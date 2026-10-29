package i18n

import (
	"fmt"
	"io"
	"log"
)

// Logf, Fatalf, Printf and Fprintf are their log/fmt namesakes in the
// process's language: the format is a Russian line, and an error among the
// arguments is translated too. A log is read by a person in a terminal, so it
// speaks the terminal's language like the rest of the output.
func Logf(format string, args ...any) { log.Print(Sprintf(format, args...)) }

func Fatalf(format string, args ...any) { log.Fatal(Sprintf(format, args...)) }

func Printf(format string, args ...any) { fmt.Print(Sprintf(format, args...)) }

func Fprintf(w io.Writer, format string, args ...any) { fmt.Fprint(w, Sprintf(format, args...)) }
