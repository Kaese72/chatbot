package main

import (
	"context"
	"net/http"
	"os"

	"github.com/Kaese72/chatbot/internal/config"
	"github.com/Kaese72/chatbot/internal/conversation"
	"github.com/Kaese72/chatbot/internal/devicestore"
	"github.com/Kaese72/chatbot/internal/events"
	"github.com/Kaese72/chatbot/internal/llm"
	"github.com/Kaese72/chatbot/internal/persistence/mariadb"
	"github.com/Kaese72/chatbot/internal/restwebapp"
	"github.com/Kaese72/chatbot/restmodels"
	log "github.com/Kaese72/huemie-lib/logging"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humamux"
	"github.com/danielgtaylor/huma/v2/sse"
	"github.com/gorilla/mux"
)

func main() {
	if err := config.Loaded.Validate(); err != nil {
		log.Error(err.Error(), map[string]interface{}{})
		os.Exit(1)
	}

	db, err := mariadb.NewMariadbPersistence(config.Loaded.Database, config.Loaded.Lock.TimeoutSeconds)
	if err != nil {
		log.Error(err.Error(), map[string]interface{}{})
		os.Exit(1)
	}

	deviceStoreClient := devicestore.NewClient(config.Loaded.DeviceStore.URL, config.Loaded.DeviceStore.JWT)
	dispatcher := llm.NewDispatcher(deviceStoreClient)
	llmClient := llm.NewClient(config.Loaded.Anthropic.Model)

	publisher, err := events.NewPublisher(config.Loaded.Event.ConnectionString)
	if err != nil {
		log.Error(err.Error(), map[string]interface{}{})
		os.Exit(1)
	}
	defer publisher.Close()

	terminations := events.NewRegistry()
	updates := events.NewRegistry()

	convService := conversation.NewService(db, llmClient, dispatcher, publisher, terminations, updates, config.Loaded.Lock.MaxToolLoopIterations)

	// Every replica must receive every ConversationEvent -- termination
	// needs to reach the one replica actually processing a conversation
	// (whichever that is), and SSE update notifications need to reach
	// every replica currently serving a .../follow/{id} connection for it.
	if err := events.StartConsumer(context.Background(), config.Loaded.Event.ConnectionString, convService); err != nil {
		log.Error(err.Error(), map[string]interface{}{})
		os.Exit(1)
	}

	webapp := restwebapp.NewWebApp(convService)

	router := mux.NewRouter()
	humaConfig := huma.DefaultConfig("chatbot-service", "0.1.0")
	humaConfig.OpenAPIPath = "/chatbot-service/openapi"
	humaConfig.DocsPath = "/chatbot-service/docs"
	api := humamux.New(router, humaConfig)

	huma.Get(api, "/chatbot-service/v0/conversations", webapp.ListConversations)
	huma.Post(api, "/chatbot-service/v0/conversations/new", webapp.NewConversation)
	huma.Get(api, "/chatbot-service/v0/conversations/{conversationID:[0-9]+}", webapp.GetConversation)
	huma.Post(api, "/chatbot-service/v0/conversations/{conversationID:[0-9]+}/input", webapp.InputConversation)
	huma.Post(api, "/chatbot-service/v0/conversations/{conversationID:[0-9]+}/terminate", webapp.TerminateConversation)
	huma.Post(api, "/chatbot-service/v0/conversations/{conversationID:[0-9]+}/forget", webapp.ForgetConversation)

	huma.Get(api, "/chatbot-service/v0/api-keys", webapp.ListAPIKeys)
	huma.Post(api, "/chatbot-service/v0/api-keys", webapp.CreateAPIKey)
	huma.Get(api, "/chatbot-service/v0/api-keys/{apiKeyID:[0-9]+}", webapp.GetAPIKey)
	huma.Patch(api, "/chatbot-service/v0/api-keys/{apiKeyID:[0-9]+}", webapp.UpdateAPIKey)
	huma.Delete(api, "/chatbot-service/v0/api-keys/{apiKeyID:[0-9]+}", webapp.DeleteAPIKey)

	sse.Register(api, huma.Operation{
		OperationID: "follow-conversation",
		Method:      http.MethodGet,
		Path:        "/chatbot-service/v0/conversations/{conversationID:[0-9]+}/follow/{dialogEntryID:[0-9]+}",
		Summary:     "Server sent events for a conversation's DialogEntries",
	}, map[string]any{"dialog-entry": restmodels.DialogEntry{}}, webapp.FollowConversation)

	log.Info("Starting chatbot-service on :8080", map[string]interface{}{})
	if err := http.ListenAndServe(":8080", router); err != nil {
		log.Error(err.Error(), map[string]interface{}{})
		os.Exit(1)
	}
}
