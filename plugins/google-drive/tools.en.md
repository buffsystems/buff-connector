<!--
What the model reads about this plugin's tools — in English; the same text in Russian is
tools.ru.md. The format is the connector's (internal/tooltext): "# tool", "## parameter";
adjacent lines join with a space, a blank line starts a new line of the text.
-->

# search_files
Find documents, spreadsheets, presentations and folders on Google Drive by words — in the name AND in
the files' text: Drive searches the contents, and the integration ranks what was found by reading the
text (words are compared by their stem, so inflected forms do not get in the way). Use it FIRST when
the question is about requirements, regulations, plans or "how things are done here"; then read what
was found with get_file.

# get_file
Read a Google Drive file in full: a document as markdown, a spreadsheet as all its sheets as tables,
a presentation as text, a folder as a list of its contents; together with its discussion. Use it when
search found a fitting file or when the question names its address.

# list_folder
List the contents of a Google Drive folder: the folders and files inside it with their kind. Use it
when the answer is put together from several files of one section.

# get_sheet
Read one sheet or a range of a Google spreadsheet (for example the sheet "Budget" or the range A1:D20
on it). Use it when the spreadsheet is large and a part of it is needed; without a sheet and a range
— the whole spreadsheet.

# get_comments
A Google Drive file's discussions: comment threads with replies, who wrote when. Use it when what has
already been discussed about this file matters.

# add_comment
Write a comment on a Google Drive file — or reply in a thread, if the key looks like FILE#comment.

# create_document
Create a Google document from markdown in a folder of the connection's area. Use it when the result
should stay on Drive as a document rather than a comment.
