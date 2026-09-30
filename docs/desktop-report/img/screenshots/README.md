# Screenshots for the desktop client report

Drop your screenshots into this folder using the exact filenames below
(PNG or JPG both work — just keep the base name and update the extension
in the `\screenshot{...}` calls if you use `.jpg`). The same filenames are
shared by both `report-en.tex` (English) and `report-ua.tex` (Ukrainian) —
add each screenshot once and it appears in both language versions.
Until a file is present, the report renders a labeled placeholder box in
its place so the document still builds.

| Filename                     | What to capture                                             |
|-------------------------------|--------------------------------------------------------------|
| `01-register.png`             | The registration screen                                      |
| `02-login.png`                | The login screen                                              |
| `03-file-list-overview.png`   | The main file list with all columns visible                  |
| `04-column-visibility.png`    | The "Columns" menu open, toggling a column on/off             |
| `05-sort-edited-by.png`       | The file list sorted by "Edited by" (show the arrow/indicator)|
| `06-filter-extension.png`     | The extension filter dropdown applied (e.g. `.cs` or `.jpg`) |
| `07-preview-java.png`         | A `.java` file opened, showing text content                  |
| `08-preview-png.png`          | A `.png` file opened, showing the image                       |
| `09-upload.png`               | An upload in progress (progress bar visible)                  |
| `10-download.png`             | Triggering / completing a file download                       |
| `11-delete.png`               | The delete confirmation / result for a file                   |
| `12-sync-panel.png`           | The folder sync panel (folder chosen, sync running)            |
| `13-sync-conflict.png`        | The sync panel showing a detected conflict with Keep Local / Keep Remote / Keep Both buttons |

After adding images, rebuild with:

```sh
cd docs/desktop-report
xelatex report-en.tex && xelatex report-en.tex
xelatex report-ua.tex && xelatex report-ua.tex
```

(run each one twice so the table of contents and figure numbers resolve).
