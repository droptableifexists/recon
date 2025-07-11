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

// Track prepared statements per connection to avoid conflicts
type ConnectionState struct {
	PreparedStatements map[string]*PreparedStatement
	LastParseMessage   *PreparedStatement
}

var connectionStates = make(map[net.Conn]*ConnectionState)

func getConnectionState(conn net.Conn) *ConnectionState {
	if state, exists := connectionStates[conn]; exists {
		return state
	}
	state := &ConnectionState{
		PreparedStatements: make(map[string]*PreparedStatement),
	}
	connectionStates[conn] = state
	return state
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
// Based on PostgreSQL protocol specification: https://www.postgresql.org/docs/current/protocol-message-formats.html
func parseBindMessage(data []byte) (portalName, statementName string, params []interface{}, err error) {
	// Debug: Print the raw data
	fmt.Printf("DEBUG: Bind message raw data (hex): %x\n", data)

	// PostgreSQL Bind message format (from official spec):
	// Int32 - Length of message contents in bytes, including self
	// String - Portal name (can be empty)
	// String - Prepared statement name (can be empty)
	// Int16 - Number of parameter format codes that follow (denoted C below)
	// Int16[C] - The parameter format codes. Each must presently be zero (text) or one (binary)
	// Int16 - Number of parameters that follow (denoted N below)
	// Int32[N] - The length of the parameter value, in bytes (this count does not include itself). Can be zero. As a special case, -1 indicates a NULL parameter value. No value bytes follow in the NULL case.
	// Byte[N] - The value of the parameter, in the format indicated by the associated format code. n is the above length.
	// Int16 - Number of result-column format codes that follow (denoted R below)
	// Int16[R] - The result-column format codes. Each must presently be zero (text) or one (binary)

	pos := 0

	// Read portal name (null-terminated string)
	portalName, pos = readNullTerminatedString(data, pos)

	// Read statement name (null-terminated string)
	statementName, pos = readNullTerminatedString(data, pos)

	fmt.Printf("DEBUG: Portal: '%s', Statement: '%s', Position: %d\n", portalName, statementName, pos)

	// Read number of parameter format codes
	if pos+2 > len(data) {
		return portalName, statementName, nil, fmt.Errorf("message too short for parameter format count")
	}
	numFormats := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	fmt.Printf("DEBUG: Number of parameter formats: %d\n", numFormats)

	// Read parameter format codes (each is 2 bytes)
	formatCodes := make([]int16, numFormats)
	if pos+numFormats*2 > len(data) {
		return portalName, statementName, nil, fmt.Errorf("message too short for parameter formats")
	}
	for i := 0; i < numFormats; i++ {
		formatCodes[i] = int16(binary.BigEndian.Uint16(data[pos : pos+2]))
		pos += 2
	}
	fmt.Printf("DEBUG: Format codes: %v\n", formatCodes)

	// Read number of parameters
	if pos+2 > len(data) {
		return portalName, statementName, nil, fmt.Errorf("message too short for parameter count")
	}
	numParams := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	fmt.Printf("DEBUG: Number of parameters: %d\n", numParams)

	// Read parameter lengths and values
	for i := 0; i < numParams; i++ {
		if pos+4 > len(data) {
			return portalName, statementName, nil, fmt.Errorf("message too short for parameter %d length", i+1)
		}

		// Read parameter length (signed int32)
		paramLength := int(int32(binary.BigEndian.Uint32(data[pos : pos+4])))
		pos += 4
		fmt.Printf("DEBUG: Parameter %d length: %d\n", i+1, paramLength)

		if paramLength == -1 {
			// NULL parameter
			params = append(params, nil)
			fmt.Printf("DEBUG: Parameter %d: NULL\n", i+1)
		} else if paramLength >= 0 {
			if pos+paramLength > len(data) {
				return portalName, statementName, nil, fmt.Errorf("message too short for parameter %d value", i+1)
			}

			// Read parameter value
			paramValue := data[pos : pos+paramLength]

			// Determine format code for this parameter
			formatCode := int16(0) // Default to text format
			if i < len(formatCodes) {
				formatCode = formatCodes[i]
			} else if len(formatCodes) == 1 {
				// If only one format code is provided, it applies to all parameters
				formatCode = formatCodes[0]
			}

			// Decode parameter based on format code
			var decodedParam interface{}
			if formatCode == 0 {
				// Text format - treat as string
				decodedParam = string(paramValue)
			} else if formatCode == 1 {
				// Binary format - try to decode as appropriate type
				decodedParam = decodeBinaryParameter(paramValue)
			} else {
				// Unknown format - treat as string
				decodedParam = string(paramValue)
			}

			params = append(params, decodedParam)
			fmt.Printf("DEBUG: Parameter %d: '%v' (length: %d, format: %d)\n", i+1, decodedParam, paramLength, formatCode)
			pos += paramLength
		} else {
			return portalName, statementName, nil, fmt.Errorf("invalid parameter %d length: %d", i+1, paramLength)
		}
	}

	fmt.Printf("DEBUG: Extracted %d parameters: %v\n", len(params), params)
	return portalName, statementName, params, nil
}

// decodeBinaryParameter attempts to decode a binary parameter value
func decodeBinaryParameter(data []byte) interface{} {
	if len(data) == 0 {
		return ""
	}

	// Try to decode as different types based on length and content
	switch len(data) {
	case 4:
		// Could be int32, float32, or other 4-byte types
		// Try as int32 first (most common)
		value := int32(binary.BigEndian.Uint32(data))
		return value
	case 8:
		// Could be int64, float64, or other 8-byte types
		// Try as int64 first
		value := int64(binary.BigEndian.Uint64(data))
		return value
	default:
		// For other lengths, try to interpret as text or return as hex
		// Check if it looks like text
		isText := true
		for _, b := range data {
			if b < 32 && b != 9 && b != 10 && b != 13 { // Not printable ASCII
				isText = false
				break
			}
		}

		if isText {
			return string(data)
		} else {
			// Return as hex string for binary data
			return fmt.Sprintf("\\x%x", data)
		}
	}
}

// readNullTerminatedString reads a null-terminated string from the data
func readNullTerminatedString(data []byte, pos int) (string, int) {
	start := pos
	for pos < len(data) && data[pos] != 0 {
		pos++
	}
	if pos < len(data) {
		pos++ // Skip the null terminator
	}
	return string(data[start : pos-1]), pos
}

// cleanParameter removes unwanted characters from parameter values
func cleanParameter(param interface{}) interface{} {
	if param == nil {
		return nil
	}

	paramStr, ok := param.(string)
	if !ok {
		return param
	}

	// Remove protocol artifacts that shouldn't be in parameter values
	cleaned := strings.ReplaceAll(paramStr, "\u0001", "") // Remove SOH (Start of Heading)
	cleaned = strings.ReplaceAll(cleaned, "\u0002", "")   // Remove STX (Start of Text)
	cleaned = strings.ReplaceAll(cleaned, "\u0003", "")   // Remove ETX (End of Text)
	cleaned = strings.ReplaceAll(cleaned, "\u0019", "")   // Remove ETB (End of Transmission Block)

	// Only clean up formatting artifacts, preserve actual data
	cleaned = strings.TrimSpace(cleaned)

	// Replace formatting whitespace but preserve null bytes and other data
	cleaned = strings.ReplaceAll(cleaned, "\t", " ") // Replace tabs with spaces
	cleaned = strings.ReplaceAll(cleaned, "\n", " ") // Replace newlines with spaces
	cleaned = strings.ReplaceAll(cleaned, "\r", " ") // Replace carriage returns with spaces

	// Collapse multiple spaces into single spaces (but only if they're formatting spaces)
	for strings.Contains(cleaned, "  ") {
		cleaned = strings.ReplaceAll(cleaned, "  ", " ")
	}

	return cleaned
}

// cleanParameterForJSON converts special characters to JSON Unicode escape sequences
func cleanParameterForJSON(param interface{}) interface{} {
	if param == nil {
		return nil
	}

	paramStr, ok := param.(string)
	if !ok {
		return param
	}

	// Convert to JSON-safe Unicode escape sequences
	cleaned := strings.ReplaceAll(paramStr, "\u0000", "\\u0000") // Null byte
	cleaned = strings.ReplaceAll(cleaned, "\n", "\\u000a")       // Newline
	cleaned = strings.ReplaceAll(cleaned, "\t", "\\u0009")       // Tab
	cleaned = strings.ReplaceAll(cleaned, "\r", "\\u000d")       // Carriage return

	return cleaned
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
		cleanedParam := cleanParameter(param)
		paramStr := fmt.Sprintf("%v", cleanedParam)
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
						getConnectionState(src).PreparedStatements[statementName] = stmt
						getConnectionState(src).LastParseMessage = stmt // Store for use with empty statement names
					}
				}
			case BindMessage:
				// Extended protocol - Bind message (parameters)
				if n > 5 {
					bindContent := buffer[5:n]
					fmt.Printf("DEBUG: Bind message content length: %d, content (hex): %x\n", len(bindContent), bindContent)
					portalName, statementName, params, err := parseBindMessage(bindContent)
					if err == nil {
						fmt.Printf("DEBUG: Bind for statement: %s, portal: %s\n", statementName, portalName)

						var stmt *PreparedStatement
						var exists bool

						// If statement name is empty, use the last Parse message
						if statementName == "" {
							stmt = getConnectionState(src).LastParseMessage
							exists = stmt != nil
						} else {
							stmt, exists = getConnectionState(src).PreparedStatements[statementName]
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
					} else {
						fmt.Printf("DEBUG: Error parsing Bind message: %v\n", err)
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
