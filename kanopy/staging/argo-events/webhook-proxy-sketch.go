// Sketch of a custom webhook proxy to unwrap Argo Events payloads
// This is NOT production-ready code, just a complexity estimate

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
)

// ArgoEventsPayload is what we receive from Argo Events HTTP trigger
type ArgoEventsPayload struct {
	Body string `json:"body"`
}

// GitHubWebhook is the unwrapped GitHub webhook payload
// We need to extract headers that were merged into the body
type GitHubWebhook struct {
	XGitHubEvent     string `json:"X-GitHub-Event"`
	XGitHubDelivery  string `json:"X-GitHub-Delivery"`
	XHubSignature256 string `json:"X-Hub-Signature-256"`
	// The rest is the actual GitHub payload
	RawPayload map[string]interface{}
}

func main() {
	targetURL := os.Getenv("TARGET_URL") // github-copier URL
	if targetURL == "" {
		log.Fatal("TARGET_URL environment variable required")
	}

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		// 1. Read Argo Events wrapped payload
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			log.Printf("Error reading body: %v", err)
			return
		}
		defer r.Body.Close()

		// 2. Unwrap the Argo Events payload
		var argoPayload ArgoEventsPayload
		if err := json.Unmarshal(body, &argoPayload); err != nil {
			http.Error(w, "Failed to parse Argo Events payload", http.StatusBadRequest)
			log.Printf("Error parsing Argo payload: %v", err)
			return
		}

		// 3. Parse the inner JSON string
		var innerPayload map[string]interface{}
		if err := json.Unmarshal([]byte(argoPayload.Body), &innerPayload); err != nil {
			http.Error(w, "Failed to parse inner payload", http.StatusBadRequest)
			log.Printf("Error parsing inner payload: %v", err)
			return
		}

		// 4. Extract GitHub headers (they were merged into the body by Argo Events)
		githubEvent, _ := innerPayload["X-GitHub-Event"].(string)
		githubDelivery, _ := innerPayload["X-GitHub-Delivery"].(string)
		hubSignature, _ := innerPayload["X-Hub-Signature-256"].(string)

		// Remove the header fields from the payload
		delete(innerPayload, "X-GitHub-Event")
		delete(innerPayload, "X-GitHub-Delivery")
		delete(innerPayload, "X-Hub-Signature-256")

		// 5. Re-encode the clean payload
		cleanPayload, err := json.Marshal(innerPayload)
		if err != nil {
			http.Error(w, "Failed to encode clean payload", http.StatusInternalServerError)
			log.Printf("Error encoding clean payload: %v", err)
			return
		}

		// 6. Forward to github-copier with proper headers
		req, err := http.NewRequest("POST", targetURL, bytes.NewReader(cleanPayload))
		if err != nil {
			http.Error(w, "Failed to create request", http.StatusInternalServerError)
			log.Printf("Error creating request: %v", err)
			return
		}

		req.Header.Set("Content-Type", "application/json")
		if githubEvent != "" {
			req.Header.Set("X-GitHub-Event", githubEvent)
		}
		if githubDelivery != "" {
			req.Header.Set("X-GitHub-Delivery", githubDelivery)
		}
		if hubSignature != "" {
			req.Header.Set("X-Hub-Signature-256", hubSignature)
		}

		// 7. Send the request
		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "Failed to forward request", http.StatusBadGateway)
			log.Printf("Error forwarding request: %v", err)
			return
		}
		defer resp.Body.Close()

		// 8. Return the response from github-copier
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)

		log.Printf("Forwarded webhook: event=%s delivery=%s status=%d",
			githubEvent, githubDelivery, resp.StatusCode)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Webhook proxy listening on port %s", port)
	log.Printf("Forwarding to: %s", targetURL)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
