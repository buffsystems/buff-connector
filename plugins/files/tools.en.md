<!--
What the model reads about this plugin's tools — in English; the same text in Russian is
tools.ru.md. The format is the connector's (internal/tooltext): "# tool", "## parameter";
adjacent lines join with a space, a blank line starts a new line of the text.
-->

# glob
Find files by name — a pattern like "*.docx", "**/*.xlsx", "contract*" (case does not matter).
Without path it searches the whole area, with path — in that folder and the ones inside it. Returns
paths, newest first. Use it when a file is needed by name or extension; grep searches the contents.
These are the COMPANY's folders on the client's disk and network shares — not the project's
repository. The connection's area: {{area}}.

## pattern
The name pattern: * ? and ** for nested folders

## path
The folder to search in; empty — the whole area

# grep
Search by contents — a regular expression over the text of the area's files (case does not matter):
Word documents, Excel spreadsheets, PowerPoint presentations (and the old .doc/.xls/.ppt), PDF and
text files are read as text. Returns the file, the line number and the line; the line number is the
same that read shows. path narrows it to a folder, include — to names ("*.pdf", "*.{doc,docx}"). Use
it FIRST when the question is about regulations, contracts, plans, figures — what is written down in
files. These are the COMPANY's folders on the client's disk and network shares — not the project's
repository. The connection's area: {{area}}.

## pattern
A regular expression (Python), case does not matter

## path
A folder or a file; empty — the whole area

## include
A file name pattern, for example *.docx or *.{xls,xlsx}

# read
Read a file or a folder by its absolute path (a local /srv/… or a network \\server\share\…,
smb://server/share/…). A Word document comes as markdown, a spreadsheet as sheets of tables, a
presentation as slides with notes, a PDF page by page, a text file as it is; lines are numbered "N:
text". A folder comes as a list of entries, folders with a slash at the end. By default a file is
read WHOLE; offset (the line to start from, from 1) and limit (how many lines) — when a part is
needed. Use it when the path is known or found with glob/grep. These are the COMPANY's folders on the
client's disk and network shares — not the project's repository. The connection's area: {{area}}.

## filePath
The absolute path to a file or a folder

## offset
The first line (from 1); empty — from the start

## limit
How many lines; empty — to the end
