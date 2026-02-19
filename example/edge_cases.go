package example

import "fmt"

// --- Case 1: Closure with @require ---

func ProcessWithCallback(db *DB) {
	handler := func(u *User) {
		// @require u != nil
		fmt.Println(u.Name)
	}

	u, _ := db.Query("SELECT 1")
	handler(u)
}

// --- Case 2: @must with custom panic message ---

func FindUser(db *DB, id string) (*User, error) {
	// @require db != nil panic("db is nil")
	// @require len(id) > 0 panic(fmt.Sprintf("invalid id: %q", id))
	user, _ := db.Query("SELECT * FROM users WHERE id = ?") // @must panic("query failed")
	return user, nil
}

// --- Case 3: Multiple directives on same function ---

func MultiCheck(a, b int, name string) {
	// @require a > 0 panic("a must be positive")
	// @require b < 1000 panic("b overflow")
	// @require len(name) > 0

	fmt.Println(a, b, name)
}

// --- Case 4: @ensure for map lookup ---

func LookupKey(m map[string]int, key string) int {
	v, _ := m[key] // @ensure panic("key not found: " + key)
	return v
}
