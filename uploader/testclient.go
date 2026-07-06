// +build ignore

// Test client to debug menu navigation
// Run with: go run testclient.go

package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
)

const maxPathDepth = 8

var (
	menuStack  [maxPathDepth]string
	menuDepth  = 0
	menuPath   = ""
	browsePath = ""

	conn   net.Conn
	reader *bufio.Reader
)

func pushPath(path string) {
	if menuDepth < maxPathDepth {
		menuStack[menuDepth] = path
		menuDepth++
		fmt.Printf("  [PUSH] depth=%d pushed='%s'\n", menuDepth, path)
	}
}

func popPath() string {
	if menuDepth > 0 {
		menuDepth--
		p := menuStack[menuDepth]
		fmt.Printf("  [POP] depth=%d popped='%s'\n", menuDepth, p)
		return p
	}
	return ""
}

func sendCommand(cmd string) {
	fmt.Fprintf(conn, "%s\n", cmd)
}

func readLine() string {
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

type MenuItem struct {
	Type  byte   // 'f', 'b', 'l'
	Name  string
	Path  string
	Count string
}

func loadMenu(path string) []MenuItem {
	fmt.Printf("\n=== MENU '%s' ===\n", path)

	var cmd string
	if path == "" {
		cmd = "MENU"
	} else {
		cmd = "MENU " + path
	}
	sendCommand(cmd)

	readLine() // OK header

	var items []MenuItem
	for {
		line := readLine()
		if line == "." {
			break
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 4 {
			items = append(items, MenuItem{
				Type:  parts[0][0],
				Name:  parts[1],
				Path:  parts[2],
				Count: parts[3],
			})
		}
	}

	menuPath = path
	fmt.Printf("  menuPath='%s', depth=%d\n", menuPath, menuDepth)

	for i, item := range items {
		fmt.Printf("  [%d] %c '%s' -> '%s'\n", i, item.Type, item.Name, item.Path)
	}

	return items
}

func loadLetters(path string) {
	fmt.Printf("\n=== LETTERS '%s' ===\n", path)
	browsePath = path

	sendCommand("LETTERS " + path)
	readLine() // OK header

	for {
		line := readLine()
		if line == "." {
			break
		}
	}

	fmt.Printf("  browsePath='%s', menuPath='%s', depth=%d\n", browsePath, menuPath, menuDepth)
}

func loadList(path string) {
	fmt.Printf("\n=== LIST '%s' ===\n", path)

	sendCommand("LISTPATH 0 5 " + path)
	readLine() // OK header

	for {
		line := readLine()
		if line == "." {
			break
		}
	}

	fmt.Printf("  menuPath='%s', depth=%d\n", menuPath, menuDepth)
}

func main() {
	var err error
	conn, err = net.Dial("tcp", "localhost:6465")
	if err != nil {
		fmt.Println("Failed to connect:", err)
		os.Exit(1)
	}
	defer conn.Close()

	reader = bufio.NewReader(conn)

	// Read greeting
	readLine()

	fmt.Println("===========================================")
	fmt.Println("Navigation Test Client")
	fmt.Println("===========================================")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  <number>  - Select menu item by index")
	fmt.Println("  b         - Go back (DEL)")
	fmt.Println("  q         - Quit")
	fmt.Println()

	stdinReader := bufio.NewReader(os.Stdin)

	// Load root menu
	items := loadMenu("")

	currentPage := "CATS" // CATS, LTRS, LIST

	for {
		fmt.Printf("\n[%s] depth=%d menuPath='%s' > ", currentPage, menuDepth, menuPath)

		input, _ := stdinReader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "q" {
			break
		}

		if input == "b" {
			// Back navigation
			switch currentPage {
			case "LIST":
				fmt.Println("\n--- BACK from LIST (no pop, reload menuPath) ---")
				items = loadMenu(menuPath)
				currentPage = "CATS"

			case "LTRS":
				fmt.Println("\n--- BACK from LTRS (no pop, reload menuPath) ---")
				items = loadMenu(menuPath)
				currentPage = "CATS"

			case "CATS":
				if menuDepth > 0 {
					fmt.Println("\n--- BACK from CATS (pop and load) ---")
					prev := popPath()
					items = loadMenu(prev)
				} else {
					fmt.Println("\n--- At root, can't go back ---")
				}
			}
			continue
		}

		// Parse number
		var idx int
		_, err := fmt.Sscanf(input, "%d", &idx)
		if err != nil || idx < 0 || idx >= len(items) {
			fmt.Println("Invalid selection")
			continue
		}

		item := items[idx]

		switch currentPage {
		case "CATS":
			switch item.Type {
			case 'f':
				fmt.Printf("\n--- SELECT folder '%s' (push menuPath, load item.Path) ---\n", item.Name)
				pushPath(menuPath)
				items = loadMenu(item.Path)
				currentPage = "CATS"

			case 'b':
				fmt.Printf("\n--- SELECT browse '%s' (NO push, load letters) ---\n", item.Name)
				// Don't push - browse is same level as menu
				loadLetters(item.Path)
				currentPage = "LTRS"

			case 'l':
				fmt.Printf("\n--- SELECT list '%s' (NO push, load list) ---\n", item.Name)
				// Don't push - list is same level as menu
				loadList(item.Path)
				currentPage = "LIST"
			}

		case "LTRS":
			fmt.Printf("\n--- SELECT letter (load list with filter) ---\n")
			loadList(browsePath)
			currentPage = "LIST"
		}
	}

	fmt.Println("Goodbye!")
}
