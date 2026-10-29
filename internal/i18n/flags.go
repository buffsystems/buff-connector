package i18n

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
)

// Flags is a flag set for the command line that speaks the process's
// language: its usage, and what the flag package itself says about a wrong
// flag — text the standard library writes in English only.
func Flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() { printUsage(current, os.Stderr, fs) }
	return fs
}

// ParseFlags parses args into fs; a wrong flag is said in the process's
// language, followed by the usage, and ends the program (-h ends it too,
// after the usage).
func ParseFlags(fs *flag.FlagSet, args []string) {
	usage := fs.Usage
	fs.Usage = func() {}
	err := fs.Parse(args)
	fs.Usage = usage
	switch {
	case err == nil:
		return
	case errors.Is(err, flag.ErrHelp):
		usage()
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, flagError(current, err.Error()))
	usage()
	os.Exit(2)
}

// flagComplaints are the flag package's own messages (Go's flag.go), each
// with the Russian line it reads as.
var flagComplaints = []struct {
	re *regexp.Regexp
	ru string
}{
	{regexp.MustCompile(`^flag provided but not defined: (-\S+)$`), "флаг %s не предусмотрен"},
	{regexp.MustCompile(`^flag needs an argument: (-\S+)$`), "флагу %s нужно значение"},
	{regexp.MustCompile(`^invalid boolean value (".*") for (-\S+): (.*)$`), "неверное логическое значение %s у флага %s: %s"},
	{regexp.MustCompile(`^invalid value (".*") for flag (-\S+): (.*)$`), "неверное значение %s у флага %s: %s"},
	{regexp.MustCompile(`^bad flag syntax: (.*)$`), "неверная запись флага: %s"},
}

// flagError is a flag package message in l. One it does not know — a newer
// Go's — is passed on as it is.
func flagError(l Lang, msg string) string {
	for _, c := range flagComplaints {
		if m := c.re.FindStringSubmatch(msg); m != nil {
			args := make([]any, len(m)-1)
			for i, v := range m[1:] {
				args[i] = v
			}
			return l.Sprintf(c.ru, args...)
		}
	}
	return msg
}

// valueWord names what a flag takes, by the flag package's own word for it.
var valueWord = map[string]string{
	"string": "текст", "int": "число", "uint": "число", "float": "число",
	"duration": "длительность", "value": "значение",
}

// printUsage is flag.PrintDefaults in l.
func printUsage(l Lang, w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, l.Sprintf("использование %s:", fs.Name()))
	fs.VisitAll(func(f *flag.Flag) {
		kind, help := flag.UnquoteUsage(f)
		line := "  -" + f.Name
		if word, ok := valueWord[kind]; ok {
			line += " " + l.T(word)
		} else if kind != "" {
			line += " " + kind
		}
		line += "\n    \t" + l.T(help)
		if f.DefValue != "" && f.DefValue != "0" && f.DefValue != "false" && f.DefValue != "0s" {
			line += l.Sprintf(" (по умолчанию %s)", f.DefValue)
		}
		fmt.Fprintln(w, line)
	})
}
