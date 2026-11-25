package desktop

import (
	"errors"
	"fmt"
	"os"
)

func Run() error        { return errors.New("desktop setup is for Windows; use hub init/serve on Linux") }
func ShowError(e error) { fmt.Fprintln(os.Stderr, e) }
