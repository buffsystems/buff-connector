<!--
What the model reads about this plugin's tools — in English; the same text in Russian is
tools.ru.md. The format is the connector's (internal/tooltext): "# tool", "## parameter";
adjacent lines join with a space, a blank line starts a new line of the text.
-->

# list_files
List the designs (Figma files) the bot is connected to: key, name, project. Use it FIRST when the
question is about a design, a screen, a layout or tokens and the question names no file key. The
area field says what the area is: if it is all the account's files, the list holds only those where
the bot was mentioned, and any other file opens by the key from a figma.com/design/KEY/… link.

# get_file
A design's structure: pages, frames and their sizes, nesting down to the given depth, the list of the
file's styles and components. Use it to understand what the file consists of and to pick a node (an
id like 1:23) for get_node. depth=1 — pages only, 2 — pages and top-level frames (the default), more
— deeper.

# get_node
A design node with its subtree and all its parameters: position and size, auto layout (direction,
padding, spacing, alignment), fills and strokes in hex, corner radii, effects, typography and texts,
bound styles and variables by name, an instance's component and its properties. Use it when the
exact values of a screen, block or element are needed (an id like 1:23 — from get_file or
search_nodes).

# get_styles
The file's design tokens: published styles with values (colours in hex, typography, effects, grids),
the styles used in the file, and variables with their values per mode (theme, size) — where the
Figma plan allows reading them. Use it when the question is about the palette, fonts, tokens or the
design system.

# search_nodes
Find design nodes by words in a layer's name or in text: returns the id, type, page and path. Use it
when the question names a screen, button, field or text whose id is unknown; then get_node. types —
an optional list of types (frame, text, instance, component).

# get_comments
A design's discussions: comment threads with replies, which node they are pinned to, who wrote when,
whether resolved. Use it when the question is about remarks, agreements or the history of a screen's
discussion.

# render_node
Render a design node to PNG and show the picture. ONLY for a model that reads images; a node's
parameters are always available through get_node without a picture. Use it when seeing the whole
composition matters, not the values. scale — the scale, 0.5–4.

# add_comment
Write a comment in a design's discussion.
