// Package dockicon shows the application icon in the macOS Dock. GLFW, and so
// Fyne, ignores window icons on macOS, and a binary started outside an .app
// bundle has no CFBundleIconFile to fall back on. Other platforms take the icon
// from the window, so Set does nothing there.
package dockicon
