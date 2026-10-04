package git

// SetBeforePush runs f just before each push, so a test can move main
// between the lander's read and its push.
func SetBeforePush(l *Lander, f func()) { l.beforePush = f }

// Dir is the lander's private repo path.
func Dir(l *Lander) string { return l.dir }
