package server

// A compact Brigadier-shaped command tree: literals and typed argument
// nodes, flattened into the 26.2 Declare Commands wire form and parsed
// with the same rules the client expects. Only the node types the
// server registers are modelled; suggestions cover literals and the
// common value sets.

import (
	"strconv"
	"strings"
	"sync"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// cmdNode is one literal or argument node.
type cmdNode struct {
	name     string
	literal  bool
	parser   string // identifier, empty for literals
	parserID int32
	props    []byte
	suggest  string
	fixedN   int // fixed token count (vec3/block_pos consume 3)
	greedy   bool

	key  string // argument name handed to the handler
	exec bool   // runnable at this node

	children []*cmdNode
	run      func(c *conn, args map[string]string) error
}

// literalf builds a literal node.
func literalf(name string) *cmdNode {
	return &cmdNode{name: name, literal: true}
}

// argf builds an argument node with vanilla parser metadata.
func argf(name, parser string, parserID int32, key string) *cmdNode {
	n := &cmdNode{name: name, parser: parser, parserID: parserID, key: key}
	switch parser {
	case "brigadier:string":
		n.greedy = true
		n.props = java.StringProps(2)
	case "minecraft:message":
		n.greedy = true
	case "minecraft:entity":
		n.props = java.EntityProps(true, true)
		n.suggest = "minecraft:ask_server"
	case "minecraft:block_pos", "minecraft:vec3":
		n.fixedN = 3
	case "brigadier:integer":
		n.props = java.IntegerProps()
	case "minecraft:time":
		n.props = java.TimeProps()
	}
	return n
}

func (n *cmdNode) add(child *cmdNode) *cmdNode {
	n.children = append(n.children, child)
	return n
}

// tokenCount reports how many tokens this argument consumes.
func (n *cmdNode) tokenCount(remaining int) int {
	if n.greedy {
		return remaining
	}
	if n.fixedN > 0 {
		return n.fixedN
	}
	return 1
}

var (
	rootOnce    sync.Once
	commandRoot *cmdNode
)

func getCommandRoot() *cmdNode {
	rootOnce.Do(func() {
		commandRoot = &cmdNode{}
		registerCommands(commandRoot)
	})
	return commandRoot
}

// flatten assigns wire indices depth-first and emits the node list.
func flatten(root *cmdNode) ([]java.CommandNodeData, int32) {
	index := map[*cmdNode]int32{}
	var order []*cmdNode
	var walk func(n *cmdNode)
	walk = func(n *cmdNode) {
		index[n] = int32(len(order))
		order = append(order, n)
		for _, ch := range n.children {
			walk(ch)
		}
	}
	walk(root)

	nodes := make([]java.CommandNodeData, len(order))
	for i, n := range order {
		var flags byte
		if n.literal {
			flags |= java.NodeFlagLiteral
		} else {
			flags |= java.NodeFlagArgument
		}
		if n.exec {
			flags |= java.NodeFlagExecutable
		}
		if n.suggest != "" {
			flags |= java.NodeFlagSuggest
		}
		data := java.CommandNodeData{
			Flags:    flags,
			Name:     n.name,
			ParserID: n.parserID,
			Props:    n.props,
			Suggest:  n.suggest,
		}
		for _, ch := range n.children {
			data.Children = append(data.Children, index[ch])
		}
		nodes[i] = data
	}
	return nodes, index[root]
}

// sendDeclareCommands pushes the tree during join.
func (c *conn) sendDeclareCommands() {
	nodes, rootIdx := flatten(getCommandRoot())
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayCommands)
	java.WriteDeclareCommands(body, nodes, rootIdx)
	_ = c.sendPacket(body.Bytes())
}

// parseTokens splits a command line: quoted strings stay one token and
// lose their quotes; backslash escapes survive.
func parseTokens(line string) []string {
	var toks []string
	var cur strings.Builder
	inQuote := false
	escaped := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case escaped:
			cur.WriteByte(ch)
			escaped = false
		case ch == '\\' && inQuote:
			escaped = true
		case ch == '"':
			inQuote = !inQuote
		case ch == ' ' && !inQuote:
			if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(ch)
		}
	}
	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	return toks
}

// parsedCommand is a successful parse: the deepest executable handler
// plus its arguments.
type parsedCommand struct {
	run  func(c *conn, args map[string]string) error
	args map[string]string
}

// tryParse resolves the deepest executable node matching the line.
// Ambiguity resolves in favour of the first matching branch.
func tryParse(root *cmdNode, line string) *parsedCommand {
	toks := parseTokens(line)
	var best *parsedCommand

	var descend func(n *cmdNode, pos int, args map[string]string)
	descend = func(n *cmdNode, pos int, args map[string]string) {
		for _, ch := range n.children {
			if ch.literal {
				if pos < len(toks) && toks[pos] == ch.name {
					if ch.exec && pos+1 == len(toks) && ch.run != nil {
						best = &parsedCommand{run: ch.run, args: cloneArgs(args)}
					}
					descend(ch, pos+1, args)
				}
				continue
			}
			count := ch.tokenCount(len(toks) - pos)
			if count <= 0 || pos+count > len(toks) {
				continue
			}
			val := strings.Join(toks[pos:pos+count], " ")
			if !validateArg(ch, val) {
				continue
			}
			next := cloneArgs(args)
			next[ch.key] = val
			if ch.exec && pos+count == len(toks) && ch.run != nil {
				best = &parsedCommand{run: ch.run, args: next}
			}
			descend(ch, pos+count, next)
		}
	}
	descend(root, 0, map[string]string{})
	return best
}

func cloneArgs(args map[string]string) map[string]string {
	out := make(map[string]string, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	return out
}

// validateArg checks the shape of one argument value.
func validateArg(n *cmdNode, val string) bool {
	switch n.parser {
	case "brigadier:integer":
		_, err := strconv.ParseInt(val, 10, 32)
		return err == nil
	case "brigadier:bool":
		return val == "true" || val == "false"
	case "minecraft:vec3", "minecraft:block_pos":
		parts := strings.Fields(val)
		if len(parts) != 3 {
			return false
		}
		for _, p := range parts {
			if strings.HasPrefix(p, "~") {
				if len(p) > 1 {
					if _, err := strconv.ParseFloat(p[1:], 64); err != nil {
						return false
					}
				}
				continue
			}
			if n.parser == "minecraft:block_pos" {
				if _, err := strconv.ParseInt(p, 10, 32); err != nil {
					return false
				}
			} else if _, err := strconv.ParseFloat(p, 64); err != nil {
				return false
			}
		}
		return true
	case "minecraft:gamemode":
		switch val {
		case "survival", "creative", "adventure", "spectator", "0", "1", "2", "3":
			return true
		}
		return false
	case "minecraft:item_stack":
		name := stripStateSuffix(val)
		_, ok := itemIDByName[name]
		return ok
	case "minecraft:block_state":
		return defaultStateOf(stripStateSuffix(val)) >= 0
	case "minecraft:entity":
		return val != ""
	}
	return true
}

// stripStateSuffix normalises "stone[foo=bar]" / "stone" to a
// namespaced identifier.
func stripStateSuffix(val string) string {
	if i := strings.IndexByte(val, '['); i >= 0 {
		val = val[:i]
	}
	if !strings.Contains(val, ":") {
		val = "minecraft:" + val
	}
	return val
}

// suggestionsFor computes tab-complete candidates for the typed text.
func suggestionsFor(text string) (start, length int32, matches []string) {
	root := getCommandRoot()
	trailing := strings.HasSuffix(text, " ")
	toks := parseTokens(text)

	// Walk as far as the tokens resolve.
	cur := root
	pos := 0
	for pos < len(toks) {
		tok := toks[pos]
		matched := false
		for _, ch := range cur.children {
			if ch.literal && tok == ch.name {
				cur = ch
				pos++
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		for _, ch := range cur.children {
			if ch.literal {
				continue
			}
			count := ch.tokenCount(len(toks) - pos)
			if count <= 0 || pos+count > len(toks) {
				continue
			}
			val := strings.Join(toks[pos:pos+count], " ")
			if !validateArg(ch, val) {
				continue
			}
			cur = ch
			pos += count
			matched = true
			break
		}
		if !matched {
			break
		}
	}

	if trailing {
		// Fresh token: offer child literals plus parser value sets.
		start = int32(len(text))
		for _, ch := range cur.children {
			if ch.literal {
				matches = append(matches, ch.name)
			}
		}
		for _, ch := range cur.children {
			if ch.literal || ch.fixedN > 0 || ch.greedy {
				continue
			}
			matches = append(matches, valueSuggestions(ch, "")...)
		}
		return start, 0, matches
	}

	if len(toks) == 0 {
		for _, ch := range root.children {
			if ch.literal {
				matches = append(matches, ch.name)
			}
		}
		return 0, 0, matches
	}
	last := toks[len(toks)-1]
	start = int32(len(text) - len(last))
	length = int32(len(last))
	for _, ch := range cur.children {
		if ch.literal && strings.HasPrefix(ch.name, last) {
			matches = append(matches, ch.name)
		}
	}
	// Value suggestions from a pending argument node.
	if !cur.literal || len(matches) == 0 {
		for _, ch := range cur.children {
			if ch.literal || ch.fixedN > 0 || ch.greedy {
				continue
			}
			matches = append(matches, valueSuggestions(ch, last)...)
		}
	}
	return start, length, matches
}

// valueSuggestions offers parser-specific candidates.
func valueSuggestions(n *cmdNode, prefix string) []string {
	var out []string
	short := func(s string) string {
		if strings.HasPrefix(s, "minecraft:") && !strings.HasPrefix(prefix, "minecraft:") {
			return s[len("minecraft:"):]
		}
		return s
	}
	switch n.parser {
	case "minecraft:item_stack":
		for name := range itemIDByName {
			if strings.HasPrefix(short(name), prefix) || strings.HasPrefix(name, prefix) {
				out = append(out, short(name))
				if len(out) >= 128 {
					return out
				}
			}
		}
	case "minecraft:gamemode":
		out = []string{"survival", "creative", "adventure", "spectator"}
	case "minecraft:block_state":
		if len(prefix) >= 1 {
			for _, b := range blockNames {
				if strings.HasPrefix(short(b), prefix) || strings.HasPrefix(b, prefix) {
					out = append(out, short(b))
					if len(out) >= 64 {
						break
					}
				}
			}
		}
	}
	return out
}
