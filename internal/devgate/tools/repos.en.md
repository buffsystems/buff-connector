<!--
What the model reads about the tools for the project's code in Buff (#372) — in English; the same
text in Russian is repos.ru.md. The format is internal/tooltext (see knowledge.en.md).
-->

# glob
Find files by a pattern in the project's code in Buff. Call it when looking for a file outside your working directory.

Search your own working directory with your own glob: this one sees only the project in Buff, paths
from its root, modules in sources/<module>. Pattern examples: **/*.ts, sources/**/config*.

## pattern
the file pattern

## path
the directory to search, from the project root; an empty string — the whole project

## hidden
true — including hidden files and directories

## limit
how many files to return; 0 — 100

## branch
the project's branch; an empty string — the main one

## cursor
0 — from the start; otherwise the number from the previous answer that said to continue

# grep
Find lines by a regular expression in the project's code in Buff. Call it before guessing about neighbouring code.

Search your own working directory with your own grep: this one sees only the project in Buff, paths
from its root. It also searches the text of Word, Excel, PowerPoint and PDF documents. The answer is
files with line numbers.

## pattern
a regular expression or exact text (see literal)

## path
a file or directory from the project root; an empty string — the whole project

## include
a file pattern, for example *.ts or *.{ts,tsx}; an empty string — all

## literal
true — pattern as exact text, not a regular expression

## caseSensitive
true — case-sensitive (the usual)

## limit
how many matching lines to return; 0 — 100

## branch
the project's branch; an empty string — the main one

## cursor
0 — from the start; otherwise the number from the previous answer that said to continue

# read
Read a file or directory from the project's code in Buff. Call it once glob or grep found the place.

Read your own files with your own read: this one sees only the project in Buff, the path from its
root. Lines are numbered; Word, Excel, PowerPoint and PDF documents read as text. Read a large file in
parts with offset and limit.

## path
the path from the project root

## offset
the line to start from (from 1); 0 — from the start

## limit
how many lines; 0 — 2000

## branch
the project's branch; an empty string — the main one

## cursor
0 — from the start; otherwise the number from the previous answer that said to continue
