package auth

// User is an authenticated identity.
//
// Table: users
type User struct {
	ID      int64
	Subject string
	Email   string
	IsAdmin bool
}
