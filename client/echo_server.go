package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
)

const (
	LogEnabled = true
	LogFile    = `echo_server.log`
	ClientHost = "127.0.0.1"
	ClientName = "LightTable-Go"
	ClientType = "go"
)

func init() {
	signal.Notify(chSig, os.Interrupt, os.Kill)

	go HandleSignals()
}

// Only one client. Client reads and writes JSON-encoded messages to LightTable.
var client net.Conn
var stop bool // Same as: var stop = false
var wg sync.WaitGroup

func main() {
	var err error

	// Setup logging
	if !LogEnabled {
		log.SetOutput(ioutil.Discard)
	} else {
		var f *os.File
		f, err = os.OpenFile(LogFile, os.O_CREATE|os.O_TRUNC, 0666)
		if err != nil {
			log.Panicf("Can't create/open logfile %q.\n", LogFile)
		} else {
			log.SetOutput(f)
		}
		defer f.Close()
	}

	var Addr = fmt.Sprintf("%s:%s", ClientHost, os.Args[1])
	client, err = net.Dial("tcp", Addr)
	if err != nil {
		log.Println(err)
		return
	}
	log.Println("Connected to", Addr)
	log.Println("Args:", os.Args)

	Start()

	Handle()
}

// First message sent to LighTable is formatted as:
// {
//     "name": "process-name",
//     "client-id": 123,
//     "dir": "/path/to/pwd",
//     "commands": ["editor.eval.mylanguage"],
//     "type": "mylanguage"
// }
//
// Note: `client-id` MUST be an integer.
type clientInfo struct {
	Name string `json:"name"`
	Cid  int    `json:"client-id"`
	Dir  string `json:"dir"`
	Cmd  string `json:"commands"`
	Type string `json:"type"`
}

// Start sends sends back to LightTable the same client-id it received
// (os.Args[2]) and other info as well such as: client name, client path,
// commands it will operate? (unconfirmed), ..
func Start() {
	var err error

	var clientId int
	clientId, err = strconv.Atoi(os.Args[2])
	if err != nil {
		log.Fatalf("[Start] Couldn't parse client-id, received %s. Error: %s\n", os.Args[2], err)
	}

	var pwd string
	pwd, err = os.Getwd()
	if err != nil {
		log.Fatalln("[Start] Couldn't get working directory. Error:", err)
	}

	var msg []byte
	msg, err = json.Marshal(clientInfo{
		Name: ClientName,
		Cid:  clientId,
		Dir:  pwd,
		Cmd:  "editor.eval.go",
		Type: ClientType,
	})
	if err != nil {
		log.Fatalln("[Start] json.Marshal error:", err)
	}

	log.Printf("Sending starting message to LightTable: %s", msg)

	buf := bufio.NewWriter(client)
	buf.Write(msg)
	buf.WriteString("\n")
	buf.Flush()
}

func Stop() {
	stop = true
	wg.Wait()
	log.Println("Stop!")
	os.Exit(0)
}

func Send(cid int, cmd string, i Info) {
	var m Message
	m.Cid = cid
	m.Cmd = cmd
	m.Info = i

	var mj = m.ToJSON()
	log.Printf("Send %s", mj)

	buf := bufio.NewWriter(client)
	buf.WriteString(mj)
	buf.WriteString("\n")
	buf.Flush()
}

func Handle() {
	buf := bufio.NewReader(client)

	for !stop {
		line, err := buf.ReadString('\n')
		if err != nil {
			continue
		}

		var m = NewMessage(line)
		if m == nil {
			continue
		}

		switch m.Cmd {
		case "client.close", "client.cancel-all":
			Stop()
		case "editor.eval.go":
			log.Println("[Handle] Got Message", m)

			wg.Add(1)
			go EvalHandler(m)
		}
	}
}

// EvalHandler sends responses to "editor.eval.go" requests.
func EvalHandler(m *Message) {
	defer wg.Done()

	var i Info

	// Echo message back
	if m.Info.Code == "" {
		m.Info.Code = "Nothing selected. Line echo not implemented yet."
	}

	i.Result = m.Info.Code
	i.Pos = m.Info.Pos

	Send(m.Cid, "editor.eval.go.result", i)
}

type Message struct {
	Cid  int
	Cmd  string
	Info Info
}

type Info struct {
	Code       string   `json:"code,omitempty"`
	LineEnding string   `json:"line-ending,omitempty"`
	Meta       *Meta    `json:"meta,omitempty"`
	Mime       string   `json:"mime,omitempty"`
	Name       string   `json:"name,omitempty"`
	Path       string   `json:"path,omitempty"`
	Pos        *Pos     `json:"pos,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	TypeName   string   `json:"type-name,omitempty"`
	Result     string   `json:"result,omitempty"`
	// Msg      string   `json:"msg,omitempty"`
	// File     string   `json:"file,omitempty"`
}

type Meta struct {
	End   int `json:"end"`
	Start int `json:"start"`
}

type Pos struct {
	Ch   int `json:"ch"`
	Line int `json:"line"`
}

// NewMessage parses LightTable's incoming JSON messages, encoded as lists, and
// parses their content into a type-safe Message.
//
// Lists always look like this: `[int,string,object]`
func NewMessage(s string) *Message {
	const NumParts = 3

	// Remove newline at end then first/last bracket
	s = strings.TrimSpace(s)
	s = s[1 : len(s)-1] //s = strings.Trim(s, "[]")

	var parts = strings.SplitN(s, ",", NumParts)
	log.Printf("[NewMessage] %d/%d parts: %s", len(parts), NumParts, parts)

	var m Message
	var field = []interface{}{
		&m.Cid, &m.Cmd, &m.Info,
	}

	// json.Unmarshal each part.
	for index := 0; index < NumParts; index++ {
		var err error

		err = json.Unmarshal([]byte(parts[index]), field[index])
		if err != nil {
			log.Printf("[NewMessage] json.Unmarshal parts[%d] failed: %s", index, err)
			return nil
		}
	}

	return &m
}

func (m Message) ToJSON() string {
	var err error
	var jsonCid, jsonCmd, jsonInfo []byte

	jsonCid, err = json.Marshal(m.Cid)
	if err != nil {
		log.Printf("[Message.ToJSON] json.Marshal of %q failed.", "m.Cid")
		return ""
	}
	jsonCmd, err = json.Marshal(m.Cmd)
	if err != nil {
		log.Printf("[Message.ToJSON] json.Marshal of %q failed.", "m.Cmd")
		return ""
	}
	jsonInfo, err = json.Marshal(m.Info)
	if err != nil {
		log.Printf("[Message.ToJSON] json.Marshal of %q failed.", "m.Info")
		return ""
	}

	return fmt.Sprintf("[%s,%s,%s]", jsonCid, jsonCmd, jsonInfo)
}

// Set up channel on which to send signal notifications.
// We must use a buffered channel or risk missing the signal
// if we're not ready to receive when the signal is sent.
var chSig = make(chan os.Signal, 1)

func HandleSignals() {
	// Block until a signal is received.
	//s := <-chSig
	<-chSig

	Stop()
	//fmt.Println("Got signal:", s)
}
