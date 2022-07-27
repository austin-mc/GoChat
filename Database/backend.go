package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
)

//address for http requests
const url = "http://localhost:8080"

//websocket upgrader
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

type User struct {
	Name   *string `json:"name"`
	UserID *int    `json:"userID"`
}

// Holds the data for a message
type Message struct {
	Sender      *string `json:"sender"`
	Epoch       *int64  `json:"epoch"`
	MessageText *string `json:"messageText"`
	RoomName    *string `json:"roomName"`
}

// HTTP Response struct containing a slice of Message
type Response struct {
	Messages []Message `json:"messages"`
}

// Global DB variable
var db *sql.DB

//All websocket connections and their userID's
var wsconns map[int]*websocket.Conn

// map[roomID]userName
var activeRooms map[int][]string

func main() {
	// Open the DB and attach it to the global variable
	DB, err := sql.Open("sqlite3", "ChatApp.db")
	db = DB
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Creating the maps
	activeRooms = make(map[int][]string)
	wsconns = make(map[int]*websocket.Conn)

	// Setting up the mux router and http handlers
	router := mux.NewRouter()
	router.HandleFunc("/status", statusCheck)

	// /chat/room/new?room-name=room%20name%20here
	router.HandleFunc("/chat/room/new", newRoomHandler).Methods("POST")

	// Experimental websocket handler
	router.HandleFunc("/chat/sockets/connect", socketHandler)

	// /chat/room/join
	// User-Name and Room-Name as header data
	router.HandleFunc("/chat/room/join", joinRoomHandler).Methods("POST")

	router.HandleFunc("/chat/room/leave", leaveRoomHandler).Methods("DELETE")

	router.HandleFunc("/chat/postmsg", newMessageHandler).Methods("POST")

	// /chat/room/(RoomName) OR /chat/room/(RoomName)?message-start-time=(Epoch)
	router.HandleFunc("/chat/room/{room}", chatHandler).Methods("GET")

	// /chat/users/new
	// User-Name as header data
	router.HandleFunc("/chat/user/new", newUserHandler).Methods("POST")

	http.Handle("/", router)

	// Using Port 8080 for now
	http.ListenAndServe(":8080", router)

}

// Handles POST requests to add users
func newUserHandler(w http.ResponseWriter, r *http.Request) {
	userInfo := User{}
	if err := json.NewDecoder(r.Body).Decode(&userInfo); err != nil {
		fmt.Println(err)
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
	if userExists(*userInfo.Name) {
		// Check if the user name already exists
		// User exists, return an error
		http.Error(w, fmt.Sprintf("Error creating user with name \"%s\": A user with this name already exists", *userInfo.Name), http.StatusBadRequest)
	} else {
		// User doesn't exist, create the room
		userID, err := newUser(*userInfo.Name)
		if err != nil {
			fmt.Println(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		userInfo.UserID = &userID
		json, err := json.Marshal(userInfo)
		if err != nil {
			log.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(json)
	}
}

func newUser(name string) (int, error) {
	res, err := db.Exec("INSERT INTO Users (Name) VALUES (?)", name)
	if err != nil {
		return 0, err
	}
	fmt.Println(res.LastInsertId())
	userID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return int(userID), nil
}

// Experimental websockets
func socketHandler(w http.ResponseWriter, r *http.Request) {
	userIDString := r.URL.Query().Get("user-id")
	c, err := upgrader.Upgrade(w, r, nil)
	go websocketListener(c)
	if err != nil {
		log.Println("Upgrader error: ", err)
		return
	}

	userID, err := strconv.Atoi(userIDString)
	if err != nil {
		log.Fatal(err)
	}
	wsconns[userID] = c

	//When the user connects, send them the last hour of messages immediately
	rows, err := db.Query("SELECT Users.Name, Epoch, MessageText, Rooms.RoomName FROM Messages INNER JOIN Users ON Messages.UserID = Users.UserID INNER JOIN Rooms ON Messages.RoomID = Rooms.RoomID WHERE Rooms.RoomName = ? AND Epoch >= ?", "TEST", time.Now().Unix()-3600)
	if err != nil {
		log.Println(err)
	}
	var nextMessage Message
	for rows.Next() {
		rows.Scan(&nextMessage.Sender, &nextMessage.Epoch, &nextMessage.MessageText, &nextMessage.RoomName)
		err = c.WriteJSON(nextMessage)
		if err != nil {
			log.Println(err)
		}
	}
}

func websocketListener(conn *websocket.Conn) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Error", fmt.Sprintf("%v", r))
		}
	}()

	var msg Message
	for {
		err := conn.ReadJSON(&msg)
		if err != nil {
			fmt.Println("Error, closing connection", err)
			conn.Close()
			//Remove the user from the map of connections
			for k, v := range wsconns {
				if v == conn {
					delete(wsconns, k)
				}
			}
		} else {
			postMessage(msg)
		}
	}
}

//Send the new message over websockets
func sendHandler(c *websocket.Conn, msg Message) {
	err := c.WriteJSON(msg)
	if err != nil {
		log.Println(err)
	}
}

// Handles the /status page to ensure the API is working
func statusCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "API is running properly")
}

// Post a new message to a room
func newMessageHandler(w http.ResponseWriter, r *http.Request) {
	userReq := Message{}
	err := json.NewDecoder(r.Body).Decode(&userReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = postMessage(userReq)

	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Post a message to the given room
func postMessage(msg Message) error {
	roomName := *msg.RoomName
	fmt.Println("Posting message to room: ", roomName, ". Message Text: ", *msg.MessageText)
	epoch := time.Now().Unix()
	if roomID, err := getRoomID(roomName); err != nil {
		log.Println(err)
	} else {
		// Use websockets to send the message to all users in the room with active connections
		for userID := range activeRooms[roomID] {
			if c, ok := wsconns[userID]; ok {
				sendHandler(c, msg)
			}
		}
	}
	res, err := db.Exec("INSERT INTO Messages (UserID, Epoch, MessageText, RoomID) VALUES ((SELECT UserID FROM Users WHERE Name = ?), ?, ?, (SELECT RoomID FROM Rooms WHERE RoomName = ?))", msg.Sender, epoch, msg.MessageText, msg.RoomName)
	if err != nil {
		log.Fatal(err)
	}

	if _, err = res.LastInsertId(); err != nil {
		return err
	}

	return nil
}

// Handles getting messages with an optional messageStartTime parameter
func chatHandler(w http.ResponseWriter, r *http.Request) {
	room := mux.Vars(r)["room"]
	messageStartTime := r.URL.Query().Get("message-start-time")
	var response Response
	var messages []Message

	if messageStartTime != "" {
		// Parse the start time into an epoch int64
		epoch, err := strconv.ParseInt(messageStartTime, 10, 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		messages, err = getMessagesAfter(room, epoch)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
	} else {
		var err error
		messages, err = getMessages(room)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
	}

	// Setting the response up in JSON format
	response.Messages = messages
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	json, err := json.Marshal(response)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

	}

	w.Write(json)
}

// Return a slice of all Messages from the given room
func getMessages(roomName string) ([]Message, error) {
	var messages []Message
	var nextMessage Message

	// Query the DB to get the username, epoch time, message text, and roomname for all messages in the room
	rows, err := db.Query("SELECT Users.Name, Epoch, MessageText, Rooms.RoomName FROM Messages INNER JOIN Users ON Messages.UserID = Users.UserID INNER JOIN Rooms ON Messages.RoomID = Rooms.RoomID WHERE Rooms.RoomName = ?", roomName)

	if err != nil {
		return nil, err
	}

	// Scan the rows and extract the data into a Message, then append it to the slice of Messages
	for rows.Next() {
		rows.Scan(&nextMessage.Sender, &nextMessage.Epoch, &nextMessage.MessageText, &nextMessage.RoomName)
		messages = append(messages, nextMessage)
	}

	return messages, nil
}

// Gets all messages in the specified room begining at the specified epoch time
func getMessagesAfter(roomName string, epoch int64) ([]Message, error) {
	var messages []Message
	var nextMessage Message

	fmt.Println("Getting messages")

	// Query the DB to get the username, epoch time, message text, and roomname for all messages in the room
	rows, err := db.Query("SELECT Users.Name, Epoch, MessageText, Rooms.RoomName FROM Messages INNER JOIN Users ON Messages.UserID = Users.UserID INNER JOIN Rooms ON Messages.RoomID = Rooms.RoomID WHERE Rooms.RoomName = ? AND Epoch >= ?", roomName, epoch)

	if err != nil {
		return nil, err
	}

	// Scan the rows and extract the data into a Message, then append it to the slice of Messages
	for rows.Next() {
		rows.Scan(&nextMessage.Sender, &nextMessage.Epoch, &nextMessage.MessageText, &nextMessage.RoomName)
		messages = append(messages, nextMessage)
	}

	return messages, nil
}

// Handles creation of new chat rooms at /chat/room/new. If the room already exists, return an error
func newRoomHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("room-name")
	//Check if the room already exists
	if roomExists(name) {
		// Room exists, return an error
		http.Error(w, fmt.Sprintf("Error creating room with name \"%s\": A room with this name already exists", name), http.StatusConflict)
	} else {
		// Room doesn't existm create a new room
		if err := createRoom(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		w.WriteHeader(http.StatusOK)
	}
}

// Creates a chat room if it doesn't already exist
func createRoom(name string) error {
	if _, err := db.Exec("INSERT OR IGNORE INTO Rooms (RoomName) VALUES (?)", name); err != nil {
		return err
	}
	return nil
}

// Check if a room exists
func roomExists(name string) bool {
	row := db.QueryRow("SELECT RoomID FROM Rooms WHERE RoomName = ?", name)
	if row.Scan() != sql.ErrNoRows {
		return true
	} else {
		return false
	}
}

// Check if a user exists
func userExists(name string) bool {
	row := db.QueryRow("SELECT * FROM Users WHERE Name = ?", name)
	if row.Scan() != sql.ErrNoRows {
		return true
	} else {
		return false
	}
}

// Sets the active status of a user when they join or leave a room
func joinRoomHandler(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("User-Name")
	room := r.Header.Get("Room-Name")

	if !userExists(user) {
		http.Error(w, fmt.Sprintf("Invalid user name supplied \"%s\": A user with this name does not exist", user), http.StatusBadRequest)
		return
	}

	if !roomExists(room) {
		if err := createRoom(room); err != nil {
			http.Error(w, fmt.Sprintf("Error creating room with name \"%s\"", room), http.StatusBadRequest)
		}
	}
	roomID, err := getRoomID(room)
	if err != nil {
		// Trying to join a room that doesn't exist, return an http error
		http.Error(w, fmt.Sprintf("Error joining room with name \"%s\"", room), http.StatusBadRequest)
		return
	}

	if _, ok := activeRooms[roomID]; !ok {
		activeRooms[roomID] = make([]string, 0)
	}

	// Add the user to the slice
	activeRooms[roomID] = append(activeRooms[roomID], user)
}

// Removes the user/room pair from ActiveRooms when they leave
func leaveRoomHandler(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("User-Name")
	room := r.Header.Get("Room-Name")

	if !userExists(user) {
		http.Error(w, fmt.Sprintf("Invalid user name supplied \"%s\": A user with this name does not exist", user), http.StatusBadRequest)
		return
	}

	roomID, err := getRoomID(room)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid room name supplied \"%s\": A room with this name does not exist", room), http.StatusBadRequest)
	}

	for i, userName := range activeRooms[roomID] {
		if userName == user {
			remove(activeRooms[roomID], i)
		}
	}

}

// Remove the element at index i from the slice
func remove(s []string, i int) []string {
	s[i] = s[len(s)-1]
	s[len(s)-1] = ""
	return s[:len(s)-1]
}

/*
NOT USING THESE FUNCTIONS CURRENTLY
*/

// Returns the roomID of the given room name
func getRoomID(roomName string) (int, error) {
	var roomID int
	err := db.QueryRow("SELECT RoomID FROM Rooms WHERE RoomName = ?", roomName).Scan(&roomID)
	if err != nil {
		return -1, err
	}
	return roomID, nil
}

//Returns a string containing the username of the given userID
func getUserByID(userID int) (string, error) {

	var userName string

	row := db.QueryRow("SELECT Name FROM Users WHERE UserID = ?", userID)
	err := row.Scan(&userName)

	if err != nil {
		return "", err
	}

	return userName, nil
}

// Returns an int containing the userID of the given username
func getUserIDByName(name *string) (int, error) {

	var userID int

	row := db.QueryRow("SELECT UserID FROM Users WHERE Name = ?", &name)
	// Scan the row, return the error if found
	if err := row.Scan(&userID); err != nil {
		return -1, err
	}

	return userID, nil
}

// Returns a slice of strings containing names of all users actively in a room
func getUsersInRoom(roomName string) ([]string, error) {
	var names []string
	var nextName string

	rows, err := db.Query(
		"SELECT Users.Name FROM ActiveRooms INNER JOIN Users ON ActiveRooms.UserID = Users.UserID INNER JOIN Rooms ON ActiveRooms.RoomID = Rooms.RoomID WHERE Rooms.RoomName = ?", roomName)

	if err != nil {
		return names, err
	}

	//Scan the returned names and return them in a slice
	for rows.Next() {
		err = rows.Scan(&nextName)
		if err != nil {
			return names, err
		}
		names = append(names, nextName)
	}
	return names, nil
}
