**TODO**  
Open work for Linux and Windows. macOS is deferred (see the end). Checks  
   
 that need physical hardware are not tracked here.  
**Deferred: macOS**  
- Privileged helper (XPC, SMAppService, signing, notarization): Phases 5–7 in  
 `docs/architecture/MACOS-CONVERGENCE.md`.  
- `OpenInstallerTarget` returns the raw `/dev/rdisk*` file directly. Raw disk  
   
 nodes need sector-aligned I/O, so wrap it with `internal/blockio` the same  
   
 way as on Windows.  
- `go vet` warns about `unsafe.Pointer` in  
 `internal/disk/darwin/native/library.go`.  
- Hotplug: the macOS backend does not implement `device.HotplugBackend`  
   
 (DiskArbitration appeared/disappeared callbacks would).  
- Click Choose ISO will crash  
- ICON no show 

   
