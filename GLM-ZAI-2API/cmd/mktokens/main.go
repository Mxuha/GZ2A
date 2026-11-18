// mktokens — rebuild a GZ2A tokens.sqlite from plain text token lines.
//
// Reads base64 z_um device tokens (one per line) from a file argument or
// stdin, dedupes them, and writes a fresh SQLite DB with the exact schema
// the bridge expects:
//
//      CREATE TABLE tokens (id INTEGER PRIMARY KEY, token TEXT);
//
// Usage:
//
//      mktokens -out tokens.sqlite tokens.txt     # from file
//      cat tokens.txt | mktokens -out tokens.db   # from stdin
//
// Written atomically: the DB is built in a temp file next to -out and
// renamed into place only on success, so a failed run never destroys the
// previous DB.
package main

import (
        "bufio"
        "database/sql"
        "flag"
        "fmt"
        "os"
        "path/filepath"
        "strings"

        _ "modernc.org/sqlite"
)

// plausibleToken reports whether s looks like a base64 z_um device token:
// ≥ 40 chars, only base64 alphabet. Junk lines are skipped so a bad paste
// can never poison the DB.
func plausibleToken(s string) bool {
        if len(s) < 40 {
                return false
        }
        for i := 0; i < len(s); i++ {
                c := s[i]
                if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
                        c == '+' || c == '/' || c == '=') {
                        return false
                }
        }
        return true
}

func main() {
        out := flag.String("out", "tokens.sqlite", "output SQLite database path")
        flag.Parse()

        in := os.Stdin
        if flag.NArg() == 1 && flag.Arg(0) != "-" {
                f, err := os.Open(flag.Arg(0))
                if err != nil {
                        fatal("open input: %v", err)
                }
                defer f.Close()
                in = f
        }

        seen := map[string]bool{}
        var tokens []string
        skipped := 0
        sc := bufio.NewScanner(in)
        sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
        for sc.Scan() {
                t := strings.TrimSpace(sc.Text())
                if t == "" || strings.HasPrefix(t, "#") {
                        continue
                }
                if !plausibleToken(t) {
                        skipped++
                        continue
                }
                if seen[t] {
                        continue
                }
                seen[t] = true
                tokens = append(tokens, t)
        }
        if err := sc.Err(); err != nil {
                fatal("read input: %v", err)
        }
        if len(tokens) == 0 {
                fatal("no valid tokens found in input (expected one base64 token per line)")
        }
        if skipped > 0 {
                fmt.Fprintf(os.Stderr, "mktokens: skipped %d junk line(s)\n", skipped)
        }

        dir := filepath.Dir(*out)
        tmp, err := os.CreateTemp(dir, ".mktokens-*")
        if err != nil {
                fatal("temp file: %v", err)
        }
        tmpName := tmp.Name()
        tmp.Close()
        os.Remove(tmpName) // sqlite driver wants to create the file itself

        db, err := sql.Open("sqlite", tmpName)
        if err != nil {
                os.Remove(tmpName)
                fatal("create db: %v", err)
        }
        if _, err := db.Exec("CREATE TABLE tokens (id INTEGER PRIMARY KEY, token TEXT);"); err != nil {
                db.Close()
                os.Remove(tmpName)
                fatal("create table: %v", err)
        }
        tx, err := db.Begin()
        if err != nil {
                db.Close()
                os.Remove(tmpName)
                fatal("begin: %v", err)
        }
        stmt, err := tx.Prepare("INSERT INTO tokens (token) VALUES (?);")
        if err != nil {
                tx.Rollback()
                db.Close()
                os.Remove(tmpName)
                fatal("prepare: %v", err)
        }
        for _, t := range tokens {
                if _, err := stmt.Exec(t); err != nil {
                        stmt.Close()
                        tx.Rollback()
                        db.Close()
                        os.Remove(tmpName)
                        fatal("insert: %v", err)
                }
        }
        stmt.Close()
        if err := tx.Commit(); err != nil {
                db.Close()
                os.Remove(tmpName)
                fatal("commit: %v", err)
        }
        db.Close()

        if err := os.Rename(tmpName, *out); err != nil {
                os.Remove(tmpName)
                fatal("rename into place: %v", err)
        }
        fmt.Printf("OK: wrote %d tokens to %s\n", len(tokens), *out)
}

func fatal(format string, args ...any) {
        fmt.Fprintf(os.Stderr, "mktokens: "+format+"\n", args...)
        os.Exit(1)
}
