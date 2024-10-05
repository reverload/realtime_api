package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

// Constants
const (
	OpenAIWebSocketURL = "wss://api.openai.com/v1/realtime?model=gpt-4o-realtime-preview-2024-10-01"
	VOICE              = "alloy"
	SYSTEM_MESSAGE     = "You are a helpful and bubbly AI assistant who loves to chat about anything the user is interested about and is prepared to offer them facts. You have a penchant for dad jokes, owl jokes, and rickrolling – subtly. Always stay positive, but work in a joke when appropriate."
)

// Global variables
var (
	openAIAPIKey string
	upgrader     = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		// Allow all origins for simplicity. Adjust in production.
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

// Session represents a connection between FreeSWITCH and OpenAI
type Session struct {
	sync.Mutex
	streamSid string
	isResponding bool
	openAIConn *websocket.Conn
	clientConn *websocket.Conn
}

// Event represents the structure of events exchanged with OpenAI
type Event struct {
	Type    string          `json:"type"`
	Session json.RawMessage `json:"session,omitempty"`
	Item    json.RawMessage `json:"item,omitempty"`
	Delta   string          `json:"delta,omitempty"`
}

// initialize loads environment variables
func initialize() {
	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found. Using environment variables.")
	}

	openAIAPIKey = os.Getenv("OPENAI_API_KEY")
	if openAIAPIKey == "" {
		log.Fatal("Missing OpenAI API key. Please set it in the environment variables.")
	}
}

func main() {
	initialize()

	router := gin.Default()

	// Route for incoming calls
	router.GET("/incoming-call", func(c *gin.Context) {
		twiml := `<?xml version="1.0" encoding="UTF-8"?>
<Response>
    <Say>Please wait while we connect your call to the AI voice assistant.</Say>
    <Pause length="1"/>
    <Say>O.K., you can start talking!</Say>
    <Connect>
        <Stream url="wss://` + c.Request.Host + `/media-stream" />
    </Connect>
</Response>`
		c.Header("Content-Type", "text/xml")
		c.String(http.StatusOK, twiml)
	})

	// WebSocket route for media-stream
	router.GET("/media-stream", func(c *gin.Context) {
		clientConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Println("WebSocket Upgrade error:", err)
			return
		}
		defer clientConn.Close()
		log.Println("Client connected")

		// Establish connection to OpenAI Realtime API
		headers := http.Header{}
		headers.Add("Authorization", "Bearer "+openAIAPIKey)
		headers.Add("OpenAI-Beta", "realtime=v1")

		openAIConn, _, err := websocket.DefaultDialer.Dial(OpenAIWebSocketURL, headers)
		if err != nil {
			log.Println("Error connecting to OpenAI Realtime API:", err)
			return
		}
		defer openAIConn.Close()
		log.Println("Connected to OpenAI Realtime API")

		session := &Session{
			clientConn: clientConn,
			openAIConn: openAIConn,
			isResponding: false,
		}

		// Send session update after connection
		session.sendSessionUpdate()

		// Start goroutines for bidirectional communication
		go session.handleOpenAIMessages()
		go session.handleClientMessages()

		// Block until connection is closed
		select {}
	})

	// Start the server
	port := os.Getenv("PORT")
	if port == "" {
		port = "5050"
	}

	router.Run(":" + port)
}

// sendSessionUpdate sends the initial session.update event to OpenAI
func (s *Session) sendSessionUpdate() {
	sessionUpdate := map[string]interface{}{
		"type": "session.update",
		"session": map[string]interface{}{
			"turn_detection": map[string]interface{}{
				"type": "server_vad",
			},
			"input_audio_format":  "g711_alaw",
			"output_audio_format": "g711_alaw",
			"voice":               VOICE,
			"instructions":        SYSTEM_MESSAGE,
			"modalities":          []string{"text", "audio"},
			"temperature":         0.8,
		},
	}

	data, err := json.Marshal(sessionUpdate)
	if err != nil {
		log.Println("Error marshaling session.update:", err)
		return
	}

	err = s.openAIConn.WriteMessage(websocket.TextMessage, data)
	if err != nil {
		log.Println("Error sending session.update:", err)
		return
	}

	log.Println("Sent session.update to OpenAI")
}

// handleOpenAIMessages listens for messages from OpenAI and forwards them to FreeSWITCH
func (s *Session) handleOpenAIMessages() {
	for {
		_, message, err := s.openAIConn.ReadMessage()
		if err != nil {
			log.Println("Error reading from OpenAI WebSocket:", err)
			return
		}

		var event Event
		err = json.Unmarshal(message, &event)
		if err != nil {
			log.Println("Error unmarshaling OpenAI message:", err)
			continue
		}

		switch event.Type {
		case "response.create":
			s.Lock()
			s.isResponding = true
			s.Unlock()
		case "response.done":
			s.Lock()
			s.isResponding = false
			s.Unlock()
		case "response.audio.delta":
			if event.Delta != "" {
				audioPayload := map[string]interface{}{
					"event":     "media",
					"streamSid": s.streamSid,
					"media": map[string]string{
						"payload": event.Delta,
					},
				}
				data, err := json.Marshal(audioPayload)
				if err != nil {
					log.Println("Error marshaling audio delta:", err)
					continue
				}
				err = s.clientConn.WriteMessage(websocket.TextMessage, data)
				if err != nil {
					log.Println("Error sending audio delta to client:", err)
					return
				}
			}
		default:
			// Log other events if necessary
			log.Printf("Received event from OpenAI: %s\n", event.Type)
		}
	}
}

// handleClientMessages listens for messages from FreeSWITCH and forwards them to OpenAI
func (s *Session) handleClientMessages() {
	for {
		_, message, err := s.clientConn.ReadMessage()
		if err != nil {
			log.Println("Error reading from client WebSocket:", err)
			return
		}

		var data map[string]interface{}
		err = json.Unmarshal(message, &data)
		if err != nil {
			log.Println("Error unmarshaling client message:", err)
			continue
		}

		eventType, ok := data["event"].(string)
		if !ok {
			log.Println("Invalid event type in client message")
			continue
		}

		switch eventType {
		case "media":
			audioPayload, ok := data["media"].(map[string]interface{})["payload"].(string)
			if !ok {
				log.Println("Invalid media payload")
				continue
			}

			// Send input_audio_buffer.append event to OpenAI
			audioAppend := map[string]interface{}{
				"type": "input_audio_buffer.append",
				"audio": audioPayload,
			}
			appendData, err := json.Marshal(audioAppend)
			if err != nil {
				log.Println("Error marshaling input_audio_buffer.append:", err)
				continue
			}
			err = s.openAIConn.WriteMessage(websocket.TextMessage, appendData)
			if err != nil {
				log.Println("Error sending input_audio_buffer.append to OpenAI:", err)
				continue
			}

			// If OpenAI is responding, interrupt the response
			s.Lock()
			if s.isResponding {
				cancelEvent := map[string]interface{}{
					"type": "response.cancel",
				}
				cancelData, err := json.Marshal(cancelEvent)
				if err != nil {
					log.Println("Error marshaling response.cancel:", err)
				} else {
					err = s.openAIConn.WriteMessage(websocket.TextMessage, cancelData)
					if err != nil {
						log.Println("Error sending response.cancel to OpenAI:", err)
					} else {
						log.Println("Sent response.cancel to OpenAI")
					}
				}
				s.isResponding = false
			}
			s.Unlock()

		case "start":
			streamSid, ok := data["start"].(map[string]interface{})["streamSid"].(string)
			if !ok {
				log.Println("Invalid streamSid in start event")
				continue
			}
			s.streamSid = streamSid
			log.Println("Incoming stream has started:", streamSid)

		default:
			log.Printf("Received non-media event from client: %s\n", eventType)
		}
	}
}
