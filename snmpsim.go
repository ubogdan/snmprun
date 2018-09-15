package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/PromonLogicalis/asn1"
	"github.com/PromonLogicalis/snmp"
)

var logger *log.Logger

// Convert OID in string format to OID in uint slice format
func strToOID(str string) (oid asn1.Oid, err error) {
	subStrings := strings.Split(str, ".")
	oid = make(asn1.Oid, len(subStrings))
	for i, component := range subStrings {
		x, err := strconv.ParseUint(component, 10, 32)
		if err != nil {
			return nil, err
		}
		oid[i] = uint(x)
	}
	return oid, nil
}

func addOIDFunc(agent *snmp.Agent, interp *Interpreter, strOid string) {
	oid, err := strToOID(strOid)
	if err != nil {
		logger.Println("Bad oid - shouldn't happen")
	}

	agent.AddRoManagedObject(
		oid,
		func(oid asn1.Oid) (interface{}, error) {
			oidStr := oid.String()
			val, found := interp.GetValueForOid(oidStr)
			if !found {
				return nil, errors.New("Illegal Value")
			}
			switch val.valueType {
			case ValueBoolean:
				return val.boolVal, nil
			case ValueInteger:
				return val.intVal, nil
			case ValueString:
				return val.stringVal, nil
			case ValueNone:
				return nil, errors.New("Illegal Value")
			}
			return nil, errors.New("Illegal Value")
		})
}

func initSNMPServer(interp *Interpreter) (agent *snmp.Agent, conn *net.UDPConn, err error) {
	agent = snmp.NewAgent()

	// Set the read-only and read-write communities
	agent.SetCommunities("public", "private")

	// Bind to an UDP port
	addr, err := net.ResolveUDPAddr("udp", ":161")
	if err != nil {
		return nil, nil, err
	}
	conn, err = net.ListenUDP("udp", addr)
	if err != nil {
		return nil, nil, err
	}

	for oidStr := range interp.oid2Values {
		addOIDFunc(agent, interp, oidStr)
	}

	return agent, conn, err
}

// Read from a channel about OID requests
func runSNMPServer(agent *snmp.Agent, conn *net.UDPConn,
	timeoutSecs uint,  quit chan bool, wg *sync.WaitGroup) {

	defer wg.Done()

	// Serve requests
	for {
		select {
        case <- quit:
            return
        default:
            // Do other stuff
        }

		buffer := make([]byte, 1024)
		conn.SetReadDeadline(time.Now().Add(timeoutSecs * time.Second)
		n, source, err := conn.ReadFrom(buffer)
		if err != nil {
			   if e, ok := err.(net.Error); !ok || !e.Timeout() {
					// handle error, it's not a timeout
					logger.Printf("Failed to read buffer: %s", err)
					os.Exit(1)
			   }
			   // timeout
			   continue
		}

		// Problem is that interpreter can produce a bunch of values
		// and we won't process them until we get a request
		// to our snmp server

		buffer, err = agent.ProcessDatagram(buffer[:n])
		if err != nil {
			logger.Println(err)
			continue
		}

		_, err = conn.WriteTo(buffer, source)
		if err != nil {
			logger.Printf("Failed to write buffer: %s", err)
			os.Exit(1)
		}
	}
}

// Program will run and will modify variables.
func runProgram(interp *Interpreter, prog *Program, quitServer chan bool, wg *sync.WaitGroup) {

	defer wg.Done()
	err := interp.InterpProgram(prog)
	if err != nil {
		logger.Printf("Interpreting error: %s\n", err)
	}
	quitServer <- true
}

func main() {
	if len(os.Args) == 1 {
		fmt.Print("Missing filename to run")
		os.Exit(1)
	}
	filename := os.Args[1]

	f, err := os.OpenFile(filename+".log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Println(err)
	}
	defer f.Close()
	logger = log.New(f, "snmpsim", log.LstdFlags)

	inputBuf, err := ioutil.ReadFile(filename)
	if err != nil {
		fmt.Printf("Unable to read file %s: %s\n", filename, err)
		os.Exit(1)
	}

	l := lex(filename, string(inputBuf))

	parser := NewParser(l)
	program, err := parser.ParseProgram()
	if err != nil {
		fmt.Printf("Parsing error: %s\n", err)
		os.Exit(1)
	}

	interp := new(Interpreter)
	interp.Init(program)

	agent, conn, err := initSNMPServer(interp)
	if err != nil {
		fmt.Printf("Failed to init snmp server: %s\n", err)
		os.Exit(1)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	readTimeoutSecs := 5

	quitServer := make(chan bool)
	go runProgram(interp, program, quitServer, &wg)
	go runSNMPServer(agent, conn, readTimeoutSecs, quitServer, &wg)

	wg.Wait()
}
