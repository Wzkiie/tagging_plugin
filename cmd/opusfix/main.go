// opusfix: Hilfsprogramm zum Testen auf dem Rechner (nicht Teil des Plugins).
// Aufruf: opusfix -album "X" -date 2024 -cover cover.jpg datei.opus [...]
package main

import (
	"flag"
	"fmt"
	"os"

	"albumfix/internal/opus"
)

func main() {
	album := flag.String("album", "", "Album, falls fehlend")
	date := flag.String("date", "", "Jahr, falls fehlend")
	cover := flag.String("cover", "", "Coverbild, falls fehlend")
	flag.Parse()
	var img []byte
	if *cover != "" {
		var err error
		if img, err = os.ReadFile(*cover); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	for _, f := range flag.Args() {
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Println(f, "FEHLER", err)
			continue
		}
		out, changed, err := opus.Apply(data, opus.Edit{Album: *album, Date: *date, Cover: img})
		switch {
		case err != nil:
			fmt.Println(f, "FEHLER", err)
		case !changed:
			fmt.Println(f, "unverändert")
		default:
			if err := os.WriteFile(f, out, 0o644); err != nil {
				fmt.Println(f, "FEHLER", err)
				continue
			}
			fmt.Println(f, "geändert")
		}
	}
}
