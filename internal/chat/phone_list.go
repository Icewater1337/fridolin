package chat

// NumbersAsNumbers returns list with each phone number in its compared form.
// An entry that is not a phone number stays as written; validation reports it.
func NumbersAsNumbers(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		if n, err := PhoneNumber(s); err == nil {
			out[i] = n
		} else {
			out[i] = s
		}
	}
	return out
}
