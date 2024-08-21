package game

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/jhshelnu/wordgame/icons"
	"github.com/jhshelnu/wordgame/words"
	"log"
	"maps"
	"os"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	TurnLimitSeconds = 20
	MaxDisplayName   = 15
)

//go:generate stringer -type gameStatus
type gameStatus int

const (
	WaitingForPlayers gameStatus = iota
	InProgress
	Over
)

type Lobby struct {
	logger *log.Logger

	Id    uuid.UUID    // the unique identifier for this lobby
	join  chan *Client // channel for new clients to join the lobby
	leave chan *Client // channel for existing clients to leave the lobby
	read  chan Message // channel for existing clients to send messages for the Lobby to read

	iconNames []string // a slice of icon file names (shuffled for each lobby)

	Status       gameStatus      // the Status of the game, indicates if its started, in progress, etc
	clients      map[int]*Client // all clients in the lobby, indexed by their id
	aliveClients []*Client       // all clients in the lobby who are not out
	turnIndex    int             // the index in aliveClients of whose turn it is

	lastClientId int // the id of the last client which connected (used to increment Client.Id's as they join the lobby)

	clientIdMutex sync.Mutex     // enforces thread-safe access to the nextClientId
	lobbyOver     chan uuid.UUID // channel that lets this lobby notify the main thread that this lobby has completed. This allows the Lobby to get GC'ed

	currentChallenge string           // the current challenge string for clientsTurn
	turnExpired      <-chan time.Time // a (read-only) channel which produces a single boolean value once the client has run out of time
}

func NewLobby(lobbyOver chan uuid.UUID) *Lobby {
	Id := uuid.New()
	logger := log.New(os.Stdout, fmt.Sprintf("Lobby [%s]: ", Id), log.Lmicroseconds|log.Lshortfile|log.Lmsgprefix)

	return &Lobby{
		logger:    logger,
		Id:        Id,
		join:      make(chan *Client),
		leave:     make(chan *Client),
		read:      make(chan Message),
		iconNames: icons.GetShuffledIconNames(),
		Status:    WaitingForPlayers,
		clients:   make(map[int]*Client),
		turnIndex: -1,
		lobbyOver: lobbyOver,
	}
}

func (lobby *Lobby) GetNextClientId() int {
	lobby.clientIdMutex.Lock()
	defer lobby.clientIdMutex.Unlock()

	lobby.lastClientId++
	return lobby.lastClientId
}

func (lobby *Lobby) GetDefaultIconName(id int) string {
	return lobby.iconNames[(id-1)%len(lobby.iconNames)]
}

func (lobby *Lobby) GetClients() []*Client {
	return slices.SortedFunc(maps.Values(lobby.clients), func(client1 *Client, client2 *Client) int { return client1.Id - client2.Id })
}

func (lobby *Lobby) StartLobby() {
	defer lobby.EndLobby()
	defer func() {
		if r := recover(); r != nil {
			lobby.logger.Printf("Encountered fatal error: %v\n%s", r, debug.Stack())
		}
	}()

	for {
		select {
		case client := <-lobby.join:
			lobby.onClientJoin(client)
		case client := <-lobby.leave:
			lobby.onClientLeave(client)
			if len(lobby.clients) == 0 {
				lobby.logger.Printf("All clients have disconnected. Goodbye.")
				return
			}
		case message := <-lobby.read:
			lobby.onMessage(message)
		case <-lobby.turnExpired:
			lobby.onTurnExpired()
		}
	}
}

func (lobby *Lobby) onClientJoin(joiningClient *Client) {
	lobby.logger.Printf("%s connected", joiningClient)

	// fill in the client on everything they missed
	joiningClient.write <- Message{Type: ClientDetails, Content: lobby.BuildClientDetails(joiningClient.Id)}

	// then add them to the lobby and broadcast that they joined to everyone (including to the new client)
	lobby.clients[joiningClient.Id] = joiningClient
	lobby.BroadcastMessage(Message{Type: ClientJoined, Content: ClientJoinedContent{
		ClientId:    joiningClient.Id,
		DisplayName: joiningClient.DisplayName,
		IconName:    joiningClient.IconName,
		// for new clients, they are considered alive if they join mid-game or after the game
		Alive: lobby.Status != InProgress,
	}})
}

func (lobby *Lobby) onClientLeave(leavingClient *Client) {
	lobby.logger.Printf("%s disconnected", leavingClient)

	delete(lobby.clients, leavingClient.Id)
	lobby.BroadcastMessage(Message{Type: ClientLeft, Content: leavingClient.Id})

	// the rest of the code in here is concerned with leaving aliveClients in a consistent state
	// if the game isn't currently in progress or the leaving client is already eliminated, then there is nothing left to do
	if lobby.Status != InProgress || !slices.Contains(lobby.aliveClients, leavingClient) {
		return
	}

	// handle game end based on leaving
	if len(lobby.aliveClients) == 2 {
		// only one client alive, we have a winner
		lobby.Status = Over

		// we're here because there are 2 clients remaining and one of them just left
		// so, the winner is the *other* one
		var winningClient *Client
		if lobby.aliveClients[0] == leavingClient {
			winningClient = lobby.aliveClients[1]
		} else {
			winningClient = lobby.aliveClients[0]
		}

		lobby.logger.Printf("Set the status to %s because %s left, which makes %s the winner", lobby.Status, leavingClient, winningClient)
		lobby.BroadcastMessage(Message{Type: GameOver, Content: winningClient.Id})
		return
	}

	// if a client leaves during their turn, remove them from the aliveClients list, and change the turn to the next client
	leavingClientTurnIndex := slices.Index(lobby.aliveClients, leavingClient)
	if leavingClientTurnIndex == lobby.turnIndex {
		lobby.logger.Printf("Changing the current turn because %s left while it was their turn", leavingClient)
		lobby.changeTurn(true)
		return
	}

	// if it's not their turn, no need to change the turn. can go ahead and remove them from aliveClients
	aliveClients := make([]*Client, 0, len(lobby.aliveClients)-1)
	for _, c := range lobby.aliveClients {
		if c.Id != leavingClient.Id {
			aliveClients = append(aliveClients, c)
		}
	}
	lobby.aliveClients = aliveClients

	// ensure turnIndex stays pointed at the same client
	if leavingClientTurnIndex < lobby.turnIndex {
		lobby.turnIndex--
	}
}

func (lobby *Lobby) onMessage(message Message) {
	switch message.Type {
	case StartGame:
		lobby.onStartGame(message)
	case RestartGame:
		lobby.onRestartGame(message)
	case AnswerPreview:
		lobby.onAnswerPreview(message)
	case SubmitAnswer:
		lobby.onAnswerSubmitted(message)
	case NameChange:
		lobby.onNameChange(message)
	default:
		lobby.logger.Printf("Received message with type %s. Ignoring due to no handler function", message.Type)
	}
}

func (lobby *Lobby) onTurnExpired() {
	// sometimes, depending on timing, our timer can fire after the players have left
	if lobby.Status != InProgress {
		lobby.logger.Printf("Ignoring %s message because lobby is in %s status", TurnExpired, lobby.Status)
		return
	}

	lobby.BroadcastMessage(Message{Type: TurnExpired, Content: lobby.aliveClients[lobby.turnIndex].Id})
	if len(lobby.aliveClients) > 2 {
		// at least 2 clients still alive, keep the game going (lobby#changeTurn will handle dropping them)
		lobby.changeTurn(true)
	} else {
		// only one client alive, we have a winner
		lobby.Status = Over

		// we're here because there are 2 clients remaining and one of them just had their turn expire
		// so, the winner is the *other* one
		losingClient := lobby.aliveClients[lobby.turnIndex]
		var winningClient *Client
		if lobby.turnIndex == 0 {
			winningClient = lobby.aliveClients[1]
		} else {
			winningClient = lobby.aliveClients[0]
		}

		lobby.aliveClients = []*Client{winningClient}

		lobby.logger.Printf("Set the status to %s because %s ran out of time, which makes %s the winner",
			lobby.Status, losingClient, winningClient)

		lobby.BroadcastMessage(Message{Type: TurnExpired, Content: losingClient.Id})
		lobby.BroadcastMessage(Message{Type: GameOver, Content: winningClient.Id})
	}
}

func (lobby *Lobby) onStartGame(message Message) {
	if lobby.Status == WaitingForPlayers && len(lobby.clients) >= 2 {
		lobby.logger.Printf("%s has started the game", lobby.clients[message.From])
		lobby.Status = InProgress
		lobby.resetAliveClients()
		lobby.changeTurn(false)
	}
}

func (lobby *Lobby) onRestartGame(message Message) {
	if lobby.Status == Over && len(lobby.clients) >= 2 {
		lobby.logger.Printf("%s has restarted the game", lobby.clients[message.From])
		lobby.resetAliveClients()
		lobby.Status = InProgress
		lobby.turnIndex = -1
		lobby.BroadcastMessage(Message{Type: RestartGame})
		lobby.changeTurn(false)
	}
}

func (lobby *Lobby) resetAliveClients() {
	// reset alive clients to hold all clients
	lobby.aliveClients = slices.SortedFunc(maps.Values(lobby.clients), func(c1 *Client, c2 *Client) int {
		return c1.Id - c2.Id
	})
}

func (lobby *Lobby) onNameChange(message Message) {
	newDisplayName, ok := message.Content.(string)
	if !ok || len(newDisplayName) > MaxDisplayName {
		return
	}

	client := lobby.clients[message.From]
	client.DisplayName = newDisplayName
	lobby.BroadcastMessage(Message{Type: NameChange, Content: ClientNameChange{ClientId: client.Id, NewDisplayName: newDisplayName}})
}

func (lobby *Lobby) onAnswerPreview(message Message) {
	if lobby.Status == InProgress && message.From == lobby.aliveClients[lobby.turnIndex].Id {
		lobby.BroadcastMessage(message)
	}
}

func (lobby *Lobby) onAnswerSubmitted(message Message) {
	if lobby.Status == InProgress && message.From == lobby.aliveClients[lobby.turnIndex].Id {
		answer, ok := message.Content.(string)
		if !ok {
			return
		}

		if !words.IsValidWord(answer) {
			lobby.logger.Printf("%s submitted '%s' for challenge '%s' - rejected because it's not a word",
				lobby.aliveClients[lobby.turnIndex], answer, lobby.currentChallenge)
			lobby.BroadcastMessage(Message{Type: AnswerRejected, Content: answer})
			return
		}

		if answer == lobby.currentChallenge {
			lobby.logger.Printf("%s submitted %s for challenge %s - rejected because it's the same as the challenge",
				lobby.aliveClients[lobby.turnIndex], answer, lobby.currentChallenge)
			lobby.BroadcastMessage(Message{Type: AnswerRejected, Content: answer})
			return
		}

		if !strings.Contains(answer, lobby.currentChallenge) {
			lobby.logger.Printf("%s submitted %s for challenge %s - rejected because it does not contain the challenge",
				lobby.aliveClients[lobby.turnIndex], answer, lobby.currentChallenge)
			lobby.BroadcastMessage(Message{Type: AnswerRejected, Content: answer})
			return
		}

		lobby.logger.Printf("%s submitted %s for challenge %s - accepted", lobby.aliveClients[lobby.turnIndex], answer, lobby.currentChallenge)
		lobby.BroadcastMessage(Message{Type: AnswerAccepted, Content: answer})
		lobby.changeTurn(false)
	}
}

// removeCurrentClient indicates if the client (whose turn it is) has gone out
// this can happen either by time running out, or by the client disconnecting
// regardless, it is the responsibility of this method to properly update the aliveClients and turnIndex variables
func (lobby *Lobby) changeTurn(removeCurrentClient bool) {
	if !removeCurrentClient {
		// if the last client didn't run out of time or disconnect, this is easy
		newTurnIndex := (lobby.turnIndex + 1) % len(lobby.aliveClients)
		if lobby.turnIndex > -1 {
			lobby.logger.Printf("Changing turn from %s to %s", lobby.aliveClients[lobby.turnIndex], lobby.aliveClients[newTurnIndex])
		} else {
			lobby.logger.Printf("Starting turn with %s", lobby.aliveClients[newTurnIndex])
		}
		lobby.turnIndex = newTurnIndex
	} else {
		eliminatedClient := lobby.aliveClients[lobby.turnIndex]
		// if they ran out of time or disconnected:
		// - kick them out of the aliveClients
		// - turnIndex can stay the same (since the next client will now occupy that index)
		//   unless the last client got eliminated, in which case just need to reset the turnIndex to 0
		aliveClients := make([]*Client, 0, len(lobby.aliveClients)-1)
		for _, c := range lobby.aliveClients {
			if c.Id != eliminatedClient.Id {
				aliveClients = append(aliveClients, c)
			}
		}
		lobby.aliveClients = aliveClients

		if lobby.turnIndex == len(lobby.aliveClients) {
			lobby.turnIndex = 0
		}

		lobby.logger.Printf("Changing turn from %s (eliminated) to %s", eliminatedClient, lobby.aliveClients[lobby.turnIndex])
	}

	lobby.currentChallenge = words.GetChallenge()
	lobby.BroadcastMessage(Message{
		Type: ClientsTurn,
		Content: ClientsTurnContent{
			ClientId:  lobby.aliveClients[lobby.turnIndex].Id,
			Challenge: lobby.currentChallenge,
			Time:      TurnLimitSeconds,
		},
	})
	lobby.turnExpired = time.After(TurnLimitSeconds * time.Second)
}

// BuildClientDetails is responsible for building and returning a ClientDetailsContent struct
// which contains the current state of the lobby for a newly connected client, so they can get caught up
func (lobby *Lobby) BuildClientDetails(joiningClientId int) ClientDetailsContent {
	// todo: send more context here: who's turn it is, what they've typed, what their challenge is, who won, etc.
	isAliveMap := make(map[*Client]bool, len(lobby.aliveClients))
	for _, c := range lobby.aliveClients {
		isAliveMap[c] = true
	}

	clientContents := make([]ClientContent, 0, len(lobby.clients))
	for _, c := range lobby.clients {
		clientContents = append(clientContents, ClientContent{
			Id:          c.Id,
			DisplayName: c.DisplayName,
			IconName:    c.IconName,
			// for existing clients, they are considered alive if the game hasn't started yet, or they are still alive in their current/last game
			Alive: lobby.Status == WaitingForPlayers || isAliveMap[c],
		})
	}

	return ClientDetailsContent{
		ClientId: joiningClientId,
		Status:   lobby.Status,
		Clients:  clientContents,
	}
}

func (lobby *Lobby) BroadcastMessage(message Message) {
	for _, c := range lobby.clients {
		c.write <- message
	}
}

func (lobby *Lobby) EndLobby() {
	lobby.lobbyOver <- lobby.Id
}
