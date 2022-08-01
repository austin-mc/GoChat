/*
A golang client implementation of a basic chat client
Allows for users to send/recieve messages in different rooms

Planned for the future:
	-Message encryption
	-Basic GUI
*/

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

//address for http requests
const url = "http://localhost:8080"
const socketURL = "ws://localhost:8080/chat/sockets/connect"

//Keeps track of userName/ID and last room the user was active in
var userInfo User
var lastActiveRoom string

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

var done chan interface{}
var interrupt chan os.Signal
var wsconn *websocket.Conn

//Keeps track of the websocket connection status
var isConnected bool

var activeRooms []Room

type Room struct {
	roomName   string
	lastUpdate int64 // Epoch of last update
}

func main() {
	activeRooms = make([]Room, 0)
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("Welcome to the golang chat app!")
	printMenu()
	fmt.Print("Please enter desired username: ")
	scanner.Scan()
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
	name := scanner.Text()
	postName(name, scanner)

	// Upgrade to websocket connection
	done = make(chan interface{})
	interrupt = make(chan os.Signal)
	signal.Notify(interrupt, os.Interrupt)
	conn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("%s?user-id=%d", socketURL, *userInfo.UserID), nil)
	wsconn = conn
	if err != nil {
		isConnected = false
	} else {
		isConnected = true
	}

	defer conn.Close()
	go recieveHandler()

	scanner.Scan()

	for scanner.Text() != "/quit" {
		if !isConnected {
			updateMessages(&activeRooms)
		}
		cmd, msg := sanitizeInput(scanner.Text())
		if err := scanner.Err(); err != nil {
			log.Fatal(err)
		}

		switch cmd {
		case "err":
		case "join":
			joinRoom(msg)
		case "leave":
			leaveRoom(msg)
		case "help":
			printMenu()
		case "quit":
			quit()
		case "msg":
			lastActiveRoom = msg
		case "status":
			printStatus()
		case "active":
			lastActiveRoom = msg
		default:
			postMessage(cmd, msg)
		}
		scanner.Scan()
	}
}

// Leaves all active rooms before quitting
func quit() {
	for _, v := range activeRooms {
		leaveRoom(v.roomName)
	}
	os.Exit(0)
}

// Handles incomoing messages over the websocket connection
func recieveHandler() {
	defer close(done)
	var msg Message
	var err error
	for {
		err = wsconn.ReadJSON(&msg)
		if err != nil {
			log.Println("Error reading json: ", err)
		}
		// Print the message to the user's console
		fmt.Printf("[%s] %s (%s): %s\n", *msg.RoomName, *msg.Sender, time.Now().Format(time.RFC822), *msg.MessageText)
	}
}

// Takes user input and splits it into a command and the text after the command
func sanitizeInput(userIn string) (string, string) {
	var command string
	var message string
	if userIn != "" && userIn[0] == '/' {
		result := strings.SplitN(userIn, " ", 2)
		command = result[0]
		if len(result) == 2 {
			message = result[1]
		}
		//remove the leading slash from the command
		command = command[1:]
	} else {
		command = ""
		message = userIn
	}
	return command, message
}

// Posts a new message to the server using websockets if available, otherwise http
func postMessage(room string, message string) {
	if room == "" {
		room = lastActiveRoom
	}
	postURL := url + "/chat/postmsg/"

	msg := Message{
		Sender:      userInfo.Name,
		MessageText: &message,
		RoomName:    &room,
	}
	err := wsconn.WriteJSON(msg)
	if err == nil {
		//Sent over WS, don't need to send over HTTP
		return
	}
	json, err := json.Marshal(msg)
	if err != nil {
		log.Fatal(err)
	}
	_, err = http.Post(postURL, "application/json", bytes.NewBuffer(json))
	if err != nil {
		log.Fatal(err)
	}
}

// Sends an HTTP POST request for the user to join a room
func joinRoom(roomName string) {
	postURL := url + "/chat/room/join"
	client := http.Client{}
	req, err := http.NewRequest("POST", postURL, nil)
	if err != nil {
		log.Println("Error joining room: ", roomName)
		return
	}
	req.Header.Set("User-Name", *userInfo.Name)
	req.Header.Set("Room-Name", roomName)
	res, err := client.Do(req)
	if err != nil || res.StatusCode == http.StatusBadRequest {
		log.Println("Error joining room: ", roomName)
		return
	} else {
		fmt.Println("Successfully joined room: ", roomName)
	}
	lastActiveRoom = roomName
	room := Room{
		roomName:   roomName,
		lastUpdate: time.Now().Unix() - 3600,
	}
	activeRooms = append(activeRooms, room)
}

// Sends an HTTP DELETE request to remove the user from the room
func leaveRoom(roomName string) {
	postURL := url + "/chat/room/leave"
	client := http.Client{}
	req, err := http.NewRequest("DELETE", postURL, nil)
	if err != nil {
		log.Println("Error joining room: ", roomName)
	}
	req.Header.Set("User-Name", *userInfo.Name)
	req.Header.Set("Room-Name", roomName)
	res, err := client.Do(req)
	if err != nil || res.StatusCode == http.StatusBadRequest {
		log.Println("Error leaving room: ", roomName)
	} else {
		fmt.Println("Successfully left room: ", roomName)
	}
	lastActiveRoom = roomName
}

// Goroutine to get and print messages from all active rooms
func updateMessages(activeRooms *[]Room) {
	//Loop indefinitely through the rooms to get updates
	for {
		for i := 0; i < len(*activeRooms); i++ {
			room := (*activeRooms)[i]
			requestURL := url + "/chat/room/" + room.roomName + "?message-start-time=" + fmt.Sprintf("%d", room.lastUpdate)
			room.lastUpdate = time.Now().Unix()
			getMessages(requestURL)
		}
		// Wait 10 seconds between requests
		time.Sleep(5 * time.Second)
	}
}

// Get messages in a room and print to console
// Format url as /chat/room/{roomName} with optional query param ?message-start-time={epoch}
func getMessages(url string) {
	resp, err := http.Get(url)
	if err != nil {
		log.Fatal(err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	var res Response
	// Make empty slice to unmarshall the JSON into
	json.Unmarshal(body, &res)
	messages := res.Messages

	for _, msg := range messages {
		fmt.Printf("[%s] %s (%s): %s\n", *msg.RoomName, *msg.Sender, time.Unix(*msg.Epoch, 0).Format(time.RFC822), *msg.MessageText)
	}
}

// Makes a GET request on /status and prints the result
func printStatus() {
	resp, err := http.Get(url + "/status")
	if err != nil {
		log.Fatal(err)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(body))
}

//Creates a new user in the database and the json response with name and userID is stored in the userInfo struct
func postName(name string, scanner *bufio.Scanner) {
	if name == "" {
		name = "default"
	}

	user := User{
		Name: &name,
	}

	jsonMsg, err := json.Marshal(user)
	if err != nil {
		log.Println("Error creating username: ", err)
		fmt.Print("Please enter a new username: ")
		scanner.Scan()
		postName(scanner.Text(), scanner)
	}
	postURL := url + "/chat/user/new"
	client := http.Client{}
	req, err := http.NewRequest("POST", postURL, bytes.NewBuffer(jsonMsg))
	if err != nil {
		log.Println("Error creating username: ", err)
		fmt.Print("Please enter a new username: ")
		scanner.Scan()
		postName(scanner.Text(), scanner)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Println("Error creating username: ", err)
		fmt.Print("Please enter a new username: ")
		scanner.Scan()
		postName(scanner.Text(), scanner)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	json.Unmarshal(body, &userInfo)
	fmt.Printf("Successfully created user: %s\n", *userInfo.Name)
}

// Prints the instructiosn for the user
func printMenu() {
	fmt.Println(">1. Type \"/join\" and a room name to join a chat room. Messages will update every 10 seconds after joining.")
	fmt.Println(">2. Type \"/leave\" and a room name to leave a chat room.")
	fmt.Println(">3. Once you have joined a room, type \"{room}\" and a message to send a new message.")
	fmt.Println(">4. Type \"/quit\" to exit the program.")
	fmt.Println(">5. Type \"/help\" at any time to view these instructions.")
	fmt.Println(">6. Type \"/status\" to see the server status.")
	fmt.Println(">6. Type \"/active\" to change the active room.")
}
