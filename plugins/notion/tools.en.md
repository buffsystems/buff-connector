<!--
What the model reads about this plugin's tools — in English; the same text in Russian is
tools.ru.md. The format is the connector's (internal/tooltext): "# tool", "## parameter";
adjacent lines join with a space, a blank line starts a new line of the text.
-->

# search_pages
Find Notion pages and databases by words — in the titles AND in the text of the area's pages (Notion
itself searches titles only; the integration reads and searches the text itself, words are compared
by their stem, so inflected forms do not get in the way). Use it FIRST when the question is about
requirements, regulations, decisions or "how things are done here"; then read what was found with
get_page or query_database.

# get_page
Read a Notion page in full — the text with headings, lists, tables and code — together with its
discussion. Use it when search found a fitting page or when the question names its address.

# get_page_children
List the pages and databases nested in a section. Use it when the answer is put together from
several pages of one section.

# get_database
The structure of a Notion database: its data sources and properties with their types. Use it before
query_database, to know which property to filter by.

# query_database
Read the records of a Notion database (by the key of the database or of a data source), optionally
filtered by the value of one property. Each record is its properties as text.

# get_comments
A Notion page's discussions: comment threads, who wrote when. Use it when what has already been
discussed about this page matters.

# add_comment
Write a comment on a Notion page — or reply in a thread, if the key looks like PAGE#discussion.

# create_page
Create a Notion child page from markdown inside the connection's area — under the given parent page.
Use it when the result should stay in Notion as a document rather than a comment.
