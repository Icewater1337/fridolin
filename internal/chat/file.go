package chat

// File is a file the bridge sends into a chat: one a brain sent with
// reply_file, from the bridge's own copy.
type File struct {
	// Path is the bridge's copy of the file, which the transport reads.
	Path string
	// Name is the file's name, which a document shows.
	Name string
	// Mime is its MIME type, without parameters.
	Mime string
	// Kind is how the chat shows it: FileImage, FileDocument or
	// FileVoice.
	Kind string
	// Caption goes with an image or a document; a voice note has none.
	Caption string
	// Seconds is how long a voice note plays.
	Seconds uint32
}

// The kinds of File.
const (
	// FileImage shows a JPEG or PNG image in the chat.
	FileImage = "image"
	// FileDocument is a file to download, shown by its name.
	FileDocument = "document"
	// FileVoice is a voice note: Ogg Opus audio, played in the chat.
	FileVoice = "voice"
)
