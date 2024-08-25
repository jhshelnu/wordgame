package main

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jhshelnu/wordgame/game"
	"github.com/jhshelnu/wordgame/icons"
	"github.com/jhshelnu/wordgame/words"
	"log"
	"net/http"
	"os"
)

var isProd = os.Getenv("PROD") != ""

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

var lobbies = make(map[uuid.UUID]*game.Lobby)
var lobbyEnded = make(chan uuid.UUID)

func createLobby(c *gin.Context) {
	lobby := game.NewLobby(lobbyEnded)
	go lobby.StartLobby()
	lobbies[lobby.Id] = lobby
	c.JSON(http.StatusCreated, gin.H{"lobbyId": lobby.Id})
}

func handleIndex(c *gin.Context) {
	c.HTML(http.StatusOK, "home.gohtml", gin.H{})
}

// navigates the user to the page for a specific lobby
func openLobby(c *gin.Context) {
	lobbyId, err := uuid.Parse(c.Param("lobbyId"))
	if err != nil {
		c.HTML(http.StatusOK, "home.gohtml", gin.H{
			"error": "Invalid lobby Id",
		})
		return
	}

	_, exists := lobbies[lobbyId]
	if !exists {
		c.HTML(http.StatusOK, "home.gohtml", gin.H{
			"error": "Lobby not found",
		})
		return
	}

	c.HTML(http.StatusOK, "lobby.gohtml", gin.H{"lobbyId": lobbyId, "isProd": isProd})
}

// once on the page for a specific lobby, the browser sends a request here to establish a WebSocket connection
// this is what actually causes the user to "join" the lobby and be able to play
func joinLobby(c *gin.Context) {
	lobbyId, err := uuid.Parse(c.Param("lobbyId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("failed to parse lobbyId: %v", err)})
		return
	}

	if _, exists := lobbies[lobbyId]; !exists {
		c.JSON(http.StatusNotFound, gin.H{"message": "Lobby not found"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Failed to upgrade ws connection: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "Failed to join lobby. An unknown error occurred when upgrading to a websocket connection.",
		})
		return
	}

	err = game.JoinClientToLobby(conn, lobbies[lobbyId])
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to join lobby. The connection was not properly added to the lobby."})
		return
	}
}

func handleEndedLobbies() {
	for {
		endedLobbyId := <-lobbyEnded
		delete(lobbies, endedLobbyId)
	}
}

func main() {
	if err := words.Init(); err != nil {
		log.Fatal(err)
	}

	if err := icons.Init(); err != nil {
		log.Fatal(err)
	}

	go handleEndedLobbies()

	if isProd {
		gin.SetMode(gin.ReleaseMode)
	}
	server := gin.New()

	// Static assets
	server.Static("/static", "./static")

	// API
	apiGroup := server.Group("/api")
	apiGroup.POST("/lobby", createLobby)

	// HTML
	server.LoadHTMLGlob("templates/*.gohtml")
	server.GET("/", handleIndex)
	server.GET("/lobby/:lobbyId", openLobby)

	// WebSocket
	server.GET("/ws/:lobbyId", joinLobby)

	if err := server.Run(); err != nil {
		log.Fatalf("Failed to start application server: %v", err)
	}
}
