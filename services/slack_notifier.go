package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SlackNotifier handles sending notifications to Slack
type SlackNotifier interface {
	// NotifyPRProcessed sends a notification when a PR is successfully processed
	NotifyPRProcessed(ctx context.Context, event *PRProcessedEvent) error

	// NotifyError sends a notification when an error occurs
	NotifyError(ctx context.Context, event *ErrorEvent) error

	// NotifyFilesCopied sends a notification when files are copied
	NotifyFilesCopied(ctx context.Context, event *FilesCopiedEvent) error

	// NotifyDeprecation sends a notification when files are deprecated
	NotifyDeprecation(ctx context.Context, event *DeprecationEvent) error

	// IsEnabled returns true if Slack notifications are enabled
	IsEnabled() bool
}

// PRProcessedEvent contains information about a processed PR
type PRProcessedEvent struct {
	PRNumber       int
	PRTitle        string
	PRURL          string
	SourceRepo     string
	TargetRepos    []string // List of target repositories files were copied to
	FilesMatched   int
	FilesCopied    int
	FilesFailed    int
	ProcessingTime time.Duration
}

// ErrorEvent contains information about an error
type ErrorEvent struct {
	Operation      string
	Error          error
	PRNumber       int
	SourceRepo     string
	DeliveryID     string // GitHub webhook delivery ID for tracing
	Attempts       int    // number of processing attempts (0 = not set)
	AdditionalInfo map[string]interface{}
}

// FilesCopiedEvent contains information about copied files
type FilesCopiedEvent struct {
	PRNumber   int
	SourceRepo string
	TargetRepo string
	FileCount  int
	Files      []string
	RuleName   string
}

// DeprecationEvent contains information about deprecated files
type DeprecationEvent struct {
	PRNumber   int
	SourceRepo string
	FileCount  int
	Files      []string
}

// DefaultSlackNotifier implements SlackNotifier using Slack webhooks
type DefaultSlackNotifier struct {
	webhookURL      string
	enabled         bool
	channel         string
	username        string
	iconEmoji       string
	httpClient      *http.Client
	plainTextOnly   bool   // If true, always use plain text (for Workflow Builder webhooks)
	messageVariable string // Variable name for Workflow Builder webhooks (default: "text")
}

// NewSlackNotifier creates a new Slack notifier
func NewSlackNotifier(webhookURL, channel, username, iconEmoji string) SlackNotifier {
	return NewSlackNotifierWithOptions(webhookURL, channel, username, iconEmoji, false, "text")
}

// NewSlackNotifierWithOptions creates a new Slack notifier with additional options
// plainTextOnly should be true when using Slack Workflow Builder webhooks,
// which don't support attachments or blocks
// messageVariable is the JSON key name for Workflow Builder webhooks (e.g., "text", "data", "message")
func NewSlackNotifierWithOptions(webhookURL, channel, username, iconEmoji string, plainTextOnly bool, messageVariable string) SlackNotifier {
	enabled := webhookURL != ""

	// Auto-detect Workflow Builder webhooks (they use /triggers/ instead of /services/)
	// and force plain text mode since they don't support attachments
	if strings.Contains(webhookURL, "/triggers/") {
		plainTextOnly = true
		LogInfo("detected Slack Workflow Builder webhook, using plain text mode")
	}

	// Default to "text" if not specified
	if messageVariable == "" {
		messageVariable = "text"
	}

	return &DefaultSlackNotifier{
		webhookURL:      webhookURL,
		enabled:         enabled,
		channel:         channel,
		username:        username,
		iconEmoji:       iconEmoji,
		plainTextOnly:   plainTextOnly,
		messageVariable: messageVariable,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// IsEnabled returns true if Slack notifications are enabled
func (sn *DefaultSlackNotifier) IsEnabled() bool {
	return sn.enabled
}

// NotifyPRProcessed sends a notification when a PR is successfully processed
func (sn *DefaultSlackNotifier) NotifyPRProcessed(ctx context.Context, event *PRProcessedEvent) error {
	if !sn.enabled {
		return nil
	}

	// Plain text format for Workflow Builder webhooks
	// Use status emoji based on success/failure
	statusEmoji := "✅"
	statusText := "Success"
	if event.FilesFailed > 0 {
		statusEmoji = "⚠️"
		statusText = "Partial"
	}

	plainText := fmt.Sprintf("%s *PR #%d* — %s\n"+
		"*Source:* %s\n",
		statusEmoji, event.PRNumber, statusText,
		event.SourceRepo)

	// Add target repos if available
	if len(event.TargetRepos) > 0 {
		if len(event.TargetRepos) == 1 {
			plainText += fmt.Sprintf("*Target:* %s\n", event.TargetRepos[0])
		} else {
			plainText += fmt.Sprintf("*Targets:* %s\n", strings.Join(event.TargetRepos, ", "))
		}
	}

	plainText += fmt.Sprintf("*Files:* %d copied", event.FilesCopied)
	if event.FilesFailed > 0 {
		plainText += fmt.Sprintf(", %d failed", event.FilesFailed)
	}

	plainText += fmt.Sprintf("\n*Time:* %s\n<%s|View PR>",
		formatDuration(event.ProcessingTime),
		event.PRURL)

	if sn.plainTextOnly {
		return sn.sendPlainText(ctx, plainText)
	}

	color := "good" // green
	if event.FilesFailed > 0 {
		color = "warning" // yellow
	}

	message := &SlackMessage{
		Channel:   sn.channel,
		Username:  sn.username,
		IconEmoji: sn.iconEmoji,
		Text:      plainText, // Fallback text
		Attachments: []SlackAttachment{
			{
				Color:     color,
				Title:     fmt.Sprintf("✅ PR #%d Processed", event.PRNumber),
				TitleLink: event.PRURL,
				Text:      event.PRTitle,
				Fields: []SlackField{
					{Title: "Repository", Value: event.SourceRepo, Short: true},
					{Title: "Files Matched", Value: fmt.Sprintf("%d", event.FilesMatched), Short: true},
					{Title: "Files Copied", Value: fmt.Sprintf("%d", event.FilesCopied), Short: true},
					{Title: "Files Failed", Value: fmt.Sprintf("%d", event.FilesFailed), Short: true},
					{Title: "Processing Time", Value: event.ProcessingTime.String(), Short: true},
				},
				Footer:     "Examples Copier",
				FooterIcon: "https://github.githubassets.com/images/modules/logos_page/GitHub-Mark.png",
				Timestamp:  time.Now().Unix(),
			},
		},
	}

	return sn.sendMessageWithFallback(ctx, message, plainText)
}

// NotifyError sends a notification when an error occurs
func (sn *DefaultSlackNotifier) NotifyError(ctx context.Context, event *ErrorEvent) error {
	if !sn.enabled {
		return nil
	}

	// Build plain text format for Workflow Builder webhooks
	plainText := fmt.Sprintf("❌ *Error* — %s", event.Operation)

	if event.PRNumber > 0 && event.SourceRepo != "" {
		plainText += fmt.Sprintf("\n*PR:* <%s/pull/%d|#%d> in %s",
			"https://github.com/"+event.SourceRepo, event.PRNumber, event.PRNumber, event.SourceRepo)
	} else if event.SourceRepo != "" {
		plainText += fmt.Sprintf("\n*Repo:* %s", event.SourceRepo)
	}

	plainText += fmt.Sprintf("\n*Error:* %s", event.Error.Error())

	if event.Attempts > 0 {
		plainText += fmt.Sprintf(" (attempt %d)", event.Attempts)
	}

	if sn.plainTextOnly {
		return sn.sendPlainText(ctx, plainText)
	}

	fields := []SlackField{
		{Title: "Operation", Value: event.Operation, Short: true},
		{Title: "Error", Value: event.Error.Error(), Short: false},
	}

	if event.SourceRepo != "" {
		fields = append(fields, SlackField{Title: "Repository", Value: event.SourceRepo, Short: true})
	}

	if event.PRNumber > 0 {
		fields = append(fields, SlackField{Title: "PR Number", Value: fmt.Sprintf("#%d", event.PRNumber), Short: true})
	}

	if event.DeliveryID != "" {
		fields = append(fields, SlackField{Title: "Delivery ID", Value: event.DeliveryID, Short: true})
	}

	if event.Attempts > 0 {
		fields = append(fields, SlackField{Title: "Attempts", Value: fmt.Sprintf("%d", event.Attempts), Short: true})
	}

	message := &SlackMessage{
		Channel:   sn.channel,
		Username:  sn.username,
		IconEmoji: sn.iconEmoji,
		Text:      plainText, // Fallback text
		Attachments: []SlackAttachment{
			{
				Color:      "danger", // red
				Title:      "❌ Error Occurred",
				Text:       fmt.Sprintf("An error occurred during %s", event.Operation),
				Fields:     fields,
				Footer:     "Examples Copier",
				FooterIcon: "https://github.githubassets.com/images/modules/logos_page/GitHub-Mark.png",
				Timestamp:  time.Now().Unix(),
			},
		},
	}

	return sn.sendMessageWithFallback(ctx, message, plainText)
}

// NotifyFilesCopied sends a notification when files are copied
func (sn *DefaultSlackNotifier) NotifyFilesCopied(ctx context.Context, event *FilesCopiedEvent) error {
	if !sn.enabled {
		return nil
	}

	// Limit files shown to first 10
	displayFiles := event.Files
	moreCount := 0
	if len(displayFiles) > 10 {
		moreCount = len(event.Files) - 10
		displayFiles = displayFiles[:10]
	}

	// Plain text format for Workflow Builder webhooks
	plainText := fmt.Sprintf("📋 *PR #%d* — %d files copied\n"+
		"*Rule:* %s\n"+
		"*Target:* %s\n"+
		"%s",
		event.PRNumber,
		event.FileCount,
		event.RuleName,
		event.TargetRepo,
		formatFileListCompact(displayFiles))
	if moreCount > 0 {
		plainText += fmt.Sprintf("_...and %d more_", moreCount)
	}

	if sn.plainTextOnly {
		return sn.sendPlainText(ctx, plainText)
	}

	filesText := ""
	if moreCount > 0 {
		filesText = fmt.Sprintf("```\n%s\n... and %d more```",
			formatFileList(displayFiles), moreCount)
	} else {
		filesText = fmt.Sprintf("```\n%s```", formatFileList(displayFiles))
	}

	message := &SlackMessage{
		Channel:   sn.channel,
		Username:  sn.username,
		IconEmoji: sn.iconEmoji,
		Text:      plainText, // Fallback text
		Attachments: []SlackAttachment{
			{
				Color: "good", // green
				Title: fmt.Sprintf("📋 Files Copied from PR #%d", event.PRNumber),
				Text:  filesText,
				Fields: []SlackField{
					{Title: "Source", Value: event.SourceRepo, Short: true},
					{Title: "Target", Value: event.TargetRepo, Short: true},
					{Title: "Rule", Value: event.RuleName, Short: true},
					{Title: "File Count", Value: fmt.Sprintf("%d", event.FileCount), Short: true},
				},
				Footer:     "Examples Copier",
				FooterIcon: "https://github.githubassets.com/images/modules/logos_page/GitHub-Mark.png",
				Timestamp:  time.Now().Unix(),
			},
		},
	}

	return sn.sendMessageWithFallback(ctx, message, plainText)
}

// NotifyDeprecation sends a notification when files are deprecated
func (sn *DefaultSlackNotifier) NotifyDeprecation(ctx context.Context, event *DeprecationEvent) error {
	if !sn.enabled {
		return nil
	}

	// Plain text format for Workflow Builder webhooks
	plainText := fmt.Sprintf("⚠️ *PR #%d* — %d files deprecated\n"+
		"*Repo:* %s\n"+
		"%s",
		event.PRNumber, event.FileCount,
		event.SourceRepo,
		formatFileListCompact(event.Files))

	if sn.plainTextOnly {
		return sn.sendPlainText(ctx, plainText)
	}

	filesText := fmt.Sprintf("```\n%s```", formatFileList(event.Files))

	message := &SlackMessage{
		Channel:   sn.channel,
		Username:  sn.username,
		IconEmoji: sn.iconEmoji,
		Text:      plainText, // Fallback text
		Attachments: []SlackAttachment{
			{
				Color: "warning", // yellow
				Title: fmt.Sprintf("⚠️ Files Deprecated from PR #%d", event.PRNumber),
				Text:  filesText,
				Fields: []SlackField{
					{Title: "Repository", Value: event.SourceRepo, Short: true},
					{Title: "File Count", Value: fmt.Sprintf("%d", event.FileCount), Short: true},
				},
				Footer:     "Examples Copier",
				FooterIcon: "https://github.githubassets.com/images/modules/logos_page/GitHub-Mark.png",
				Timestamp:  time.Now().Unix(),
			},
		},
	}

	return sn.sendMessageWithFallback(ctx, message, plainText)
}

// sendPlainText sends a plain text message to Slack
// For Workflow Builder webhooks (/triggers/), this sends a simple object that
// the workflow can use as input variables
func (sn *DefaultSlackNotifier) sendPlainText(ctx context.Context, text string) error {
	// For Workflow Builder webhooks, we send a simple key-value payload
	// using the configured variable name (e.g., "text", "data", "message")
	payload, err := json.Marshal(map[string]string{sn.messageVariable: text})
	if err != nil {
		return fmt.Errorf("failed to marshal slack message: %w", err)
	}

	return sn.sendPayload(ctx, payload)
}

// sendMessageWithFallback tries to send a rich message first, then falls back to plain text
// if the webhook doesn't support attachments (e.g., Workflow Builder webhooks)
func (sn *DefaultSlackNotifier) sendMessageWithFallback(ctx context.Context, message *SlackMessage, plainText string) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal slack message: %w", err)
	}

	err = sn.sendPayload(ctx, payload)
	if err != nil {
		// If the rich message failed, try plain text as fallback
		// This handles Workflow Builder webhooks that don't support attachments
		LogInfo("rich slack message failed, trying plain text fallback", "error", err.Error())
		return sn.sendPlainText(ctx, plainText)
	}

	return nil
}

// sendPayload sends the raw JSON payload to Slack
func (sn *DefaultSlackNotifier) sendPayload(ctx context.Context, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, "POST", sn.webhookURL, bytes.NewBuffer(payload)) // #nosec G107 G704 -- URL is the Slack webhook URL from trusted server config, not user input
	if err != nil {
		return fmt.Errorf("failed to create slack request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := sn.httpClient.Do(req) // #nosec G107 G704 -- URL is the Slack webhook URL from trusted server config, not user input
	if err != nil {
		return fmt.Errorf("failed to send slack message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack returned non-200 status: %d", resp.StatusCode)
	}

	return nil
}

// formatFileList formats a list of files for display (verbose, with bullets)
func formatFileList(files []string) string {
	result := ""
	for _, file := range files {
		result += "• " + file + "\n"
	}
	return result
}

// formatFileListCompact formats files in a compact inline format for plain text messages
func formatFileListCompact(files []string) string {
	if len(files) == 0 {
		return ""
	}
	result := "`"
	for i, file := range files {
		if i > 0 {
			result += "`, `"
		}
		result += file
	}
	result += "`\n"
	return result
}

// formatDuration formats a duration in a human-readable way
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%.1fm", d.Minutes())
}

// SlackMessage represents a Slack message
type SlackMessage struct {
	Channel     string            `json:"channel,omitempty"`
	Username    string            `json:"username,omitempty"`
	IconEmoji   string            `json:"icon_emoji,omitempty"`
	Text        string            `json:"text,omitempty"`
	Attachments []SlackAttachment `json:"attachments,omitempty"`
}

// SlackAttachment represents a Slack message attachment
type SlackAttachment struct {
	Color      string       `json:"color,omitempty"`
	Title      string       `json:"title,omitempty"`
	TitleLink  string       `json:"title_link,omitempty"`
	Text       string       `json:"text,omitempty"`
	Fields     []SlackField `json:"fields,omitempty"`
	Footer     string       `json:"footer,omitempty"`
	FooterIcon string       `json:"footer_icon,omitempty"`
	Timestamp  int64        `json:"ts,omitempty"`
}

// SlackField represents a field in a Slack attachment
type SlackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}
