package shortcuts

type shorter interface {
	getShortCode() string
	isValidCode() bool
}
