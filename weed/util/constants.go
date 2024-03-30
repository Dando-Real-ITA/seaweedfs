package util

import (
	"fmt"
)

const HttpStatusCancelled = 499

var (
	VERSION_NUMBER  = fmt.Sprintf("%.02f", 3.64)
	VERSION         = sizeLimit + " " + VERSION_NUMBER
	COMMIT          = ""
	PRIVATE         = "Katapy"
	PRIVATE_VERSION = ""
)

func Version() string {
	return VERSION + " " + COMMIT + " ( " + PRIVATE + " " + PRIVATE_VERSION + " )"
}
