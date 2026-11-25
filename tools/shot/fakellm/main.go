// Command fakellm is a deterministic local fake of an OpenAI-compatible chat
// server, for the illustrative agent scenes of tools/shot/scenes.sh (16b and
// 18). It costs nothing and calls nothing: the first request gets a fixed
// list_vaults {sort: sharpe} tool call, and the second a reply built from the
// tool result hlp sends back. Screenshots made with it are captioned
// illustrative.
//
//	go run ./tools/shot/fakellm PORT [LOGFILE]
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type msg struct {
	Role    string  `json:"role"`
	Content *string `json:"content"`
}

func findRows(v any, out *[]map[string]any) {
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t["name"]; ok {
			if _, ok2 := t["sharpe"]; ok2 {
				*out = append(*out, t)
				return
			}
		}
		for _, x := range t {
			findRows(x, out)
		}
	case []any:
		for _, x := range t {
			findRows(x, out)
		}
	}
}

func num(v any) float64 { f, _ := v.(float64); return f }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: fakellm PORT [LOGFILE]")
		os.Exit(2)
	}
	logf := io.Discard
	if len(os.Args) > 2 {
		f, err := os.Create(os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		logf = f
	}
	http.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"fake-local"}]}`)
	})
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []msg `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var tool string
		for _, m := range req.Messages {
			if m.Role == "tool" && m.Content != nil {
				tool = *m.Content
			}
		}
		fmt.Fprintf(logf, "request: %d msgs, tool=%d bytes\n", len(req.Messages), len(tool))
		w.Header().Set("Content-Type", "application/json")
		if tool == "" {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_vaults","arguments":"{\"sort\":\"sharpe\",\"limit\":8}"}}]}}],"usage":{"prompt_tokens":1200,"completion_tokens":24}}`)
			return
		}
		fmt.Fprintf(logf, "tool content head: %s\n", tool)
		var rows []map[string]any
		rn := regexp.MustCompile(`"name":"([^"]+)"`)
		rd := regexp.MustCompile(`"maxDD":(-?[0-9.e-]+)`)
		rs := regexp.MustCompile(`"sharpe":(-?[0-9.e-]+)`)
		for _, chunk := range strings.Split(tool, `"address":`)[1:] {
			n, d, sh := rn.FindStringSubmatch(chunk), rd.FindStringSubmatch(chunk), rs.FindStringSubmatch(chunk)
			if n == nil || sh == nil {
				continue
			}
			r := map[string]any{"name": n[1]}
			r["sharpe"], _ = strconv.ParseFloat(sh[1], 64)
			if d != nil {
				f, _ := strconv.ParseFloat(d[1], 64)
				r["maxDd"] = f * 100
			}
			rows = append(rows, r)
		}
		sort.SliceStable(rows, func(i, j int) bool { return num(rows[i]["sharpe"]) > num(rows[j]["sharpe"]) })
		// The follow-up rule the agent's system prompt asks for (DESIGN.md
		// 5.2 Agent replies): the card says the numbers, the reply adds at
		// most two sentences and never repeats them.
		var b strings.Builder
		if len(rows) == 0 {
			b.WriteString("The leaders pair steady returns with shallow drawdowns. Check capacity and age before sizing in.")
		} else {
			name, _ := rows[0]["name"].(string)
			slug := strings.ToLower(strings.Fields(name + " vault")[0])
			if m := regexp.MustCompile(`\(([A-Za-z0-9]+)\)`).FindStringSubmatch(name); m != nil {
				slug = strings.ToLower(m[1])
			}
			fmt.Fprintf(&b, "%s leads on risk-adjusted terms, with the shallowest drawdowns in the list. ", name)
			fmt.Fprintf(&b, "Open /vault %s and read its checklist before you deposit.", slug)
		}
		out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": b.String()}}}, "usage": map[string]int{"prompt_tokens": 3400, "completion_tokens": 120}})
		w.Write(out)
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:"+os.Args[1], nil))
}
