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
	// Debug: Print the raw data
	fmt.Printf("DEBUG: Bind message raw data (hex): %x\n", data)

	// Find null-terminated strings
	parts := bytes.Split(data, []byte{0})
	if len(parts) < 2 {
		return "", "", nil, fmt.Errorf("invalid bind message format")
	}

	portalName = string(parts[0])
	statementName = string(parts[1])
	fmt.Printf("DEBUG: Portal: %s, Statement: %s\n", portalName, statementName)

	// For now, let's try a simpler approach - look for parameter values in the remaining parts
	// Skip the first two parts (portal_name and statement_name)
	for i := 2; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			// This might be a parameter value
			paramStr := string(parts[i])

			// Filter out binary data and protocol artifacts
			// Only include strings that look like actual parameter values
			if len(paramStr) > 0 && len(paramStr) < 100 {
				// Check if the string contains mostly printable characters
				printableCount := 0
				for _, r := range paramStr {
					if r >= 32 && r <= 126 { // Printable ASCII range
						printableCount++
					}
				}

				// If more than 80% of characters are printable, consider it a parameter
				if float64(printableCount)/float64(len(paramStr)) > 0.8 {
					// Additional filter: exclude single characters that are likely protocol data
					if len(paramStr) > 1 || (len(paramStr) == 1 && (paramStr[0] >= '0' && paramStr[0] <= '9' || paramStr[0] >= 'a' && paramStr[0] <= 'z' || paramStr[0] >= 'A' && paramStr[0] <= 'Z')) {
						params = append(params, paramStr)
						fmt.Printf("DEBUG: Found parameter: %s\n", paramStr)
					}
				}
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
