package gensecrets

type Entry struct {
	Name         string   `json:"name"`
	Key          string   `json:"key"`
	Length       int      `json:"length"`
	Special      bool     `json:"special"`
	Destinations []string `json:"destinations"`
}
