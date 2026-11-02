//go:build wasip1

package fsutil

// Unter WASI gibt es kein chmod/chown, eine neu angelegte Datei bekäme 0600 und einen neuen Inode - für Samba oder
// andere Nutzer wäre sie dann unlesbar. Deshalb wird der Inhalt in die vorhandene Datei kopiert.
func replace(tmp, orig string) error { return copyReplace(tmp, orig) }
