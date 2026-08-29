GoFlasher for Windows x64

Windows distribution is portable-only. This is a portable application; no
installation is required.

1. Extract the ZIP before running.
2. Run GoFlasher.exe as Administrator for disk writing and formatting.

Writing or formatting erases the selected target. Only removable USB devices
meeting the GoFlasher safety policy are shown.

GoFlasher does not automatically update itself. Download future versions
manually from the official GitHub Releases page.

To verify the downloaded ZIP, compare this Command Prompt result with the
adjacent .sha256 file before extracting:

  certutil -hashfile GoFlasher-VERSION-windows-amd64.zip SHA256

This build is not code signed. Windows may show a SmartScreen prompt and an
"Unknown publisher" confirmation when you run it. Verify the SHA-256 above,
then choose More info -> Run anyway and confirm the prompt.

Official releases: https://github.com/goflasher/goflasher/releases
