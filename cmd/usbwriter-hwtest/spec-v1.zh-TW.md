# GoFlasher 硬體驗收規格 v1

[English](spec-v1.md)

**Protocol identifier：** `goflasher-hwtest/v1`  
**狀態：** 每個stable release candidate必須執行  
**範圍：** 實體硬體上的Linux、Windows及macOS production backend

## 安全gate

1. 測試媒體必須由tester擁有、不含所需資料，並明確標示為可丟棄。不得連接包含OS、home data或任何資料唯一副本的disk。
2. Reviewer依 `allowlist.example.json` 準備JSON allowlist。每筆必須包含精確backend identity、capacity、model、可取得時的serial及 `"disposable": true`，並與release evidence一同保存。
3. 每次執行都需 `--allowlist`；device-specific invocation另需 `--device-id`。Harness絕不選第一顆disk，也不接受path、drive letter或disk number作為selection input。
4. 每個destructive case前執行 `--case prepare`。核對identity與實體label，再透過 `--confirmation` 傳入完整 `ERASE <identity> <random-nonce>`。Challenge於15分鐘後過期，state file在I/O前刪除，不能重用。
5. 使用deterministic、不可壓縮的256 MiB v1 image，前4 bytes不得為 `00 ff 47 46`，並記錄byte length與SHA-256。Removal/cancellation另用至少4 GiB image，確保可在完成前介入。

## 共用command

從精確RC commit建置：

```sh
go build -trimpath -o usbwriter-hwtest ./cmd/usbwriter-hwtest
./usbwriter-hwtest --allowlist approved-devices.json --case inventory
```

每個destructive case前重新prepare：

```sh
./usbwriter-hwtest --allowlist approved-devices.json \
  --device-id 'EXACT_ID' --case prepare --challenge-file challenge.json
# 複製完整輸出字串；不可用script產生或推導。
```

Destructive command再加入 `--challenge-file challenge.json --confirmation 'ERASE …'`。PASS line只是必要而非充分證據；必須保存完整stdout/stderr及下列平台證據。

## 每個平台的必要case

| ID | 程序 | 必要結果 |
|---|---|---|
| HW-01 | 無target時inventory；插入allowlisted media後 `enumerate-present`；拔除後 `enumerate-absent`；在同一port重插再 `enumerate-present`。 | 相同backend identity回復；能觀察absence；不會暗中選device。另記錄換port後的平台特定identity結果。 |
| HW-02 | 保存第一次snapshot。移除後插入另一支allowlisted disposable device，讓OS重用Linux/macOS node或Windows disk number，再執行 `path-reuse`。 | 重用locator顯示不同identity，舊identity未被選中；必須重試至真的觀察到reuse。 |
| HW-03 | 在disposable media建立並mount partition，再執行 `write-verify-eject`。 | open前卸載全部target partition；不改動無關mount；write成功。 |
| HW-04 | 移除test media，擷取GUI/harness inventory及顯示boot/system disk與active swap/pagefile的平台metadata。 | System及swap disk不在allowed inventory；不對它們執行destructive operation。 |
| HW-05 | 對large image執行 `write-cancel`，deadline在write途中觸發。 | non-zero/cancel；不可顯示flush/verify/eject成功；GUI仍responsive；partial media仍視為可丟棄。 |
| HW-06 | 執行 `write-remove`，progress開始後只拔掉target。 | I/O fail closed；不得報completed/verified；重插後必須重新enumerate及confirm。 |
| HW-07 | 對v1 image執行 `write-verify-eject`。 | unmount、完整write、explicit flush、限定byte count的SHA-256 read-back及平台eject全部成功。 |
| HW-08 | 執行 `corruption-detect`：write/verify後經backend改前4 bytes、flush，再對原SHA-256 read-back。 | `PASS: deliberate corruption detected`；mismatch絕不可報verified。 |
| HW-09 | HW-07 eject後檢查實體與OS狀態並安全拔除。 | Linux/macOS顯示ejected或powered off；Windows native safe removal成功；無mounted partition。 |

`write-remove`會先印拔除提示再開始。若image先完成則case失敗，須用更大image重測。Cancellation與removal是分開case。

## 平台證據command

在相關case前後記錄；公開副本可遮無關serial，但受限release archive須保留未遮版本。這些command僅供人工證據收集，production backend不得呼叫它們。

### Linux

```sh
uname -a; cat /etc/os-release
lsblk -b -O -J
findmnt --json
cat /proc/swaps
journalctl --since '10 minutes ago' -u polkit --no-pager
```

確認polkit prompt顯示GoFlasher、GUI UID不是0、helper由root擁有，且 `/usr/libexec/goflasher-helper` 沒有setuid。

### Windows

```powershell
Get-ComputerInfo | Select-Object WindowsProductName,WindowsVersion,OsBuildNumber
Get-Disk | Format-List Number,FriendlyName,SerialNumber,UniqueId,Size,BusType,IsBoot,IsSystem,IsOffline
Get-Partition | Format-List DiskNumber,PartitionNumber,DriveLetter,IsBoot,IsSystem
Get-Volume | Format-List DriveLetter,FileSystemLabel,Size
```

記錄UAC/admin context，證明只有選定disk被lock、dismount及安全移除。Production backend不執行PowerShell。

### macOS

```sh
sw_vers
diskutil list -plist > diskutil-list-before.plist
diskutil info -plist /dev/diskN > target-info-before.plist
mount
sysctl vm.swapusage
```

記錄對應post-case plist與 `diskutil eject` 結果。

## 驗收

Linux、Windows與macOS都必須使用RC binary及 `docs/development/TESTING.zh-TW.md` 核准matrix，分別通過HW-01至HW-09。任一failure、skip、未真正觀察address reuse、缺證據或只在VM測試都會阻擋stable release。只有經文件化security review並更新本versioned spec，才能把case標為not applicable。
