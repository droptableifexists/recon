package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/droptableifexists/recon/sql-proxy/api"
	"github.com/droptableifexists/recon/sql-proxy/store"
)

// PostgreSQL message types
const (
	QueryMessage    = 'Q'
	ParseMessage    = 'P'
	BindMessage     = 'B'
	ExecuteMessage  = 'E'
	DescribeMessage = 'D'
	CloseMessage    = 'C'
	SyncMessage     = 'S'
)

// Track prepared statements for extended protocol
type PreparedStatement struct {
	Name   string
	Query  string
	Params []interface{}
}

var preparedStatements = make(map[string]*PreparedStatement)
var lastParseMessage *PreparedStatement // Track the most recent Parse message

// parsePostgreSQLMessage parses a PostgreSQL protocol message
func parsePostgreSQLMessage(data []byte) (messageType byte, content []byte, err error) {
	if len(data) < 5 {
		return 0, nil, fmt.Errorf("message too short")
	}

	messageType = data[0]
	messageLength := binary.BigEndian.Uint32(data[1:5])

	if len(data) < int(messageLength) {
		return 0, nil, fmt.Errorf("incomplete message")
	}

	content = data[5:messageLength]
	return messageType, content, nil
}

// parseParseMessage extracts the query from a Parse message
func parseParseMessage(data []byte) (statementName, query string, err error) {
	// Parse message format: statement_name\0query\0num_param_types
	parts := bytes.Split(data, []byte{0})
	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid parse message format")
	}

	statementName = string(parts[0])
	query = string(parts[1])
	return statementName, query, nil
}

// parseBindMessage extracts parameters from a Bind message
func parseBindMessage(data []byte) (portalName, statementName string, params []interface{}, err error) {
	// Bind message format: portal_name\0statement_name\0num_param_formats\0param_formats\0num_params\0param_lengths\0param_values
	parts := bytes.Split(data, []byte{0})
	if len(parts) < 2 {
		return "", "", nil, fmt.Errorf("invalid bind message format")
	}

	portalName = string(parts[0])
	statementName = string(parts[1])

	// Debug: Print the raw data
	fmt.Printf("DEBUG: Bind message raw data (hex): %x\n", data)
	fmt.Printf("DEBUG: Portal: %s, Statement: %s\n", portalName, statementName)

	// The PostgreSQL Bind message has a complex binary format
	// Let's try a different approach - look for the parameter values directly in the binary data

	// Find the position after the null-terminated strings
	pos := 0
	for i := 0; i < 2; i++ { // Skip portal_name and statement_name
		pos += len(parts[i]) + 1 // +1 for the null terminator
	}

	// Skip parameter format info
	if pos < len(data) {
		// Skip num_param_formats (2 bytes)
		pos += 2
	}

	// Skip parameter formats if any
	if pos < len(data) {
		numFormats := int(binary.BigEndian.Uint16(data[pos-2 : pos]))
		pos += numFormats * 2 // Each format is 2 bytes
	}

	// Read number of parameters
	if pos+2 <= len(data) {
		numParams := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		pos += 2

		fmt.Printf("DEBUG: Number of parameters: %d\n", numParams)

		// Skip parameter lengths
		pos += numParams * 4 // Each length is 4 bytes

		// Now read the actual parameter values
		for i := 0; i < numParams && pos+4 <= len(data); i++ {
			paramLength := int(binary.BigEndian.Uint32(data[pos : pos+4]))
			pos += 4

			if paramLength == -1 {
				// NULL parameter
				params = append(params, nil)
				fmt.Printf("DEBUG: Parameter %d: NULL\n", i+1)
			} else if pos+paramLength <= len(data) {
				// Read the parameter value
				paramValue := data[pos : pos+paramLength]
				params = append(params, string(paramValue))
				fmt.Printf("DEBUG: Parameter %d: %s (length: %d)\n", i+1, string(paramValue), paramLength)
				pos += paramLength
			} else {
				fmt.Printf("DEBUG: Parameter %d: Invalid length %d at pos %d (data len: %d)\n", i+1, paramLength, pos, len(data))
			}
		}
	}

	fmt.Printf("DEBUG: Extracted %d parameters: %v\n", len(params), params)
	return portalName, statementName, params, nil
}

// formatParameterizedQuery combines a query with its parameters
func formatParameterizedQuery(query string, params []interface{}) string {
	if len(params) == 0 {
		return query
	}

	// Simple parameter substitution for display
	result := query
	for i, param := range params {
		placeholder := fmt.Sprintf("$%d", i+1)
		paramStr := fmt.Sprintf("%v", param)
		result = strings.ReplaceAll(result, placeholder, paramStr)
	}
	return result
}

func main() {
	// Get configuration from environment variables with defaults
	listenPort := getEnv("LISTEN_PORT", "5433")
	backendHost := getEnv("BACKEND_HOST", "postgres")
	backendPort := getEnv("BACKEND_PORT", "5432")
	passThroughPort := getEnv("PASS_THROUGH_PORT", "5434")

	// The address on which our proxy listens
	listenAddr := ":" + listenPort
	// The actual Postgres server address
	backendAddr := backendHost + ":" + backendPort
	// port to pass through to the original Postgres server
	passThroughAddr := ":" + passThroughPort

	qs := store.MakeQueryStore()
	a := api.MakeQueriesExecutedAPI(qs)
	go a.RunApi()

	// Start the monitored proxy (with query interception)
	go startMonitoredProxy(listenAddr, backendAddr, qs)

	// Start the passthrough proxy (no interception)
	go startPassthroughProxy(passThroughAddr, backendAddr)

	// Keep the main goroutine alive
	select {}
}

func startMonitoredProxy(listenAddr, backendAddr string, qs *store.QueryStore) {
	// Listen for incoming client connections
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Printf("Failed to listen on monitored port: %v", err)
		return
	}
	defer listener.Close()
	fmt.Printf("Monitored proxy listening on %s, forwarding to %s\n", listenAddr, backendAddr)

	// Handle incoming client connections
	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}
		go handleMonitoredClient(clientConn, backendAddr, qs)
	}
}

func startPassthroughProxy(listenAddr, backendAddr string) {
	// Listen for incoming client connections
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Printf("Failed to listen on passthrough port: %v", err)
		return
	}
	defer listener.Close()
	fmt.Printf("Passthrough proxy listening on %s, forwarding to %s\n", listenAddr, backendAddr)

	// Handle incoming client connections
	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}
		go handlePassthroughClient(clientConn, backendAddr)
	}
}

func handleMonitoredClient(clientConn net.Conn, backendAddr string, qs *store.QueryStore) {
	defer clientConn.Close()

	// Connect to the backend (Postgres server)
	backendConn, err := net.Dial("tcp", backendAddr)
	if err != nil {
		log.Printf("Failed to connect to backend: %v", err)
		return
	}
	defer backendConn.Close()

	// Proxy data from client to backend
	go listenAndProxyData(clientConn, backendConn, qs)
	// Proxy data from backend to client
	listenAndProxyData(backendConn, clientConn, qs)
}

func handlePassthroughClient(clientConn net.Conn, backendAddr string) {
	defer clientConn.Close()

	// Connect to the backend (Postgres server)
	backendConn, err := net.Dial("tcp", backendAddr)
	if err != nil {
		log.Printf("Failed to connect to backend: %v", err)
		return
	}
	defer backendConn.Close()

	// Proxy data from client to backend (no processing)
	go dontListenAndProxyData(clientConn, backendConn)
	// Proxy data from backend to client (no processing)
	dontListenAndProxyData(backendConn, clientConn)
}

// listenAndProxyData forwards data between two connections
func listenAndProxyData(src net.Conn, dst net.Conn, qs *store.QueryStore) {
	buffer := make([]byte, 4096)
	for {
		// Read data from source
		n, err := src.Read(buffer)
		if err != nil {
			log.Printf("Error reading from source: %v", err)
			return
		}

		// Process the message if it's a PostgreSQL protocol message
		if n > 0 {
			messageType := buffer[0]
			fmt.Printf("DEBUG: Processing message type: %c (0x%02x)\n", messageType, messageType)

			switch messageType {
			case QueryMessage:
				// Simple query protocol
				if n > 5 {
					queryContent := bytes.Trim(buffer[5:n], "\x00")
					query := string(queryContent)
					if query != "" {
						fmt.Printf("Simple Query: %s\n", query)
						qs.AddQuery(store.QueryExecuted{
							Query: query,
						})
					}
				}
			case ParseMessage:
				// Extended protocol - Parse message
				if n > 5 {
					parseContent := buffer[5:n]
					statementName, query, err := parseParseMessage(parseContent)
					if err == nil && query != "" {
						fmt.Printf("Parse Statement: %s -> %s\n", statementName, query)
						stmt := &PreparedStatement{
							Name:  statementName,
							Query: query,
						}
						preparedStatements[statementName] = stmt
						lastParseMessage = stmt // Store for use with empty statement names
					}
				}
			case BindMessage:
				// Extended protocol - Bind message (parameters)
				if n > 5 {
					bindContent := buffer[5:n]
					portalName, statementName, params, err := parseBindMessage(bindContent)
					if err == nil {
						fmt.Printf("DEBUG: Bind for statement: %s, portal: %s\n", statementName, portalName)

						var stmt *PreparedStatement
						var exists bool

						// If statement name is empty, use the last Parse message
						if statementName == "" {
							stmt = lastParseMessage
							exists = stmt != nil
						} else {
							stmt, exists = preparedStatements[statementName]
						}

						if exists {
							stmt.Params = params
							formattedQuery := formatParameterizedQuery(stmt.Query, params)
							fmt.Printf("Bind Parameters: %s -> %s\n", portalName, formattedQuery)
							qs.AddQuery(store.QueryExecuted{
								Query: formattedQuery,
							})
						} else {
							fmt.Printf("DEBUG: Statement %s not found in prepared statements\n", statementName)
						}
					}
				}
			case ExecuteMessage:
				// Extended protocol - Execute message
				fmt.Printf("Execute Statement\n")
			}
		}

		// Write data to destination
		_, err = dst.Write(buffer[:n])
		if err != nil {
			log.Printf("Error writing to destination: %v", err)
			return
		}
	}
}

func dontListenAndProxyData(src net.Conn, dst net.Conn) {
	buffer := make([]byte, 4096)
	for {
		// Read data from source
		n, err := src.Read(buffer)
		if err != nil {
			log.Printf("Error reading from source: %v", err)
			return
		}
		// Write data to destination
		_, err = dst.Write(buffer[:n])
		if err != nil {
			log.Printf("Error writing to destination: %v", err)
			return
		}
	}
}

// Helper function to get environment variables with defaults
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
