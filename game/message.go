package game

type MessageType string

const (
	START_GAME         = "start_game"         // the game has started
	CLIENT_ID_ASSIGNED = "client_id_assigned" // sent to a newly connected client, indicating their id
	CLIENT_JOINED      = "client_joined"      // a new client has joined
	CLIENT_LEFT        = "client_left"        // a client has left
	SUBMIT_ANSWER      = "submit_answer"      // when the client submits an answer
	ANSWER_PREVIEW     = "answer_preview"     // preview of the current answer (not submitted) so other clients can see
	ANSWER_ACCEPTED    = "answer_accepted"    // the answer is accepted
	ANSWER_REJECTED    = "answer_rejected"    // the answer is not accepted
	TURN_EXPIRED       = "turn_expired"       // client has run out of time
	CLIENTS_TURN       = "clients_turn"       // it's a new clients turn
	GAME_OVER          = "game_over"          // the game is over
	RESTART_GAME       = "restart_game"       // sent from a client to initiate a game restart. sever then rebroadcasts to all clients to confirm
	NAME_CHANGE        = "name_change"        // used by clients to indicate they want a new display name
)

type Message struct {
	From    int         // id of the Client in the lobby
	Type    MessageType // content of the message
	Content any         // any additional info, e.g. which client joined, what their answer is, etc
}

type ClientsTurnContent struct {
	ClientId  int    // whose turn it is
	Challenge string // what the challenge string is, e.g. "atr"
}

type ClientJoinedContent struct {
	ClientId    int    // the id of the newly joined client
	DisplayName string // what their name is
	IconName    string // which icon they are using
}

type ClientNameChange struct {
	ClientId       int    // who is changing their name
	NewDisplayName string // what they are changing their name to
}
