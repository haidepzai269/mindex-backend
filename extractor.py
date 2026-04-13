import json
import sys
import os
from pathlib import Path
from dataclasses import dataclass, asdict
from typing import Literal

@dataclass
class Block:
    type: str # "heading1", "heading2", "heading3", "paragraph", "table", "code", "formula", "list_item", "caption", "empty"
    content: str
    page: int = 0
    level: int = 0

def extract_docx(path: str) -> list[Block]:
    from docx import Document
    
    doc = Document(path)
    blocks = []

    for para in doc.paragraphs:
        text = para.text.strip()
        if not text:
            continue

        style_name = para.style.name if para.style else "normal"
        style = style_name.lower()

        if "heading 1" in style:
            blocks.append(Block("heading1", text, level=1))
        elif "heading 2" in style:
            blocks.append(Block("heading2", text, level=2))
        elif "heading 3" in style:
            blocks.append(Block("heading3", text, level=3))
        elif style in ("list paragraph", "list bullet", "list number"):
            blocks.append(Block("list_item", text))
        elif style == "caption":
            blocks.append(Block("caption", text))
        elif "title" in style:
             blocks.append(Block("heading1", text, level=1))
        elif "subtitle" in style:
             blocks.append(Block("heading2", text, level=2))
        else:
            # Fallback for Bold Normal text acting as heading (naive heuristic)
            if style == "normal" and len(text) < 100:
                is_bold = all(run.bold for run in para.runs if run.text.strip())
                if is_bold and len(para.runs) > 0:
                     blocks.append(Block("heading3", text, level=3)) # Treat bold standalone normal text as heading 3
                     continue
            
            # Detect code block via font
            if para.runs and para.runs[0].font and para.runs[0].font.name in ("Courier New", "Consolas", "Courier", "Monaco"):
                blocks.append(Block("code", text))
            else:
                blocks.append(Block("paragraph", text))

    for table in doc.tables:
        rows = []
        for row in table.rows:
            cells = [cell.text.strip() for cell in row.cells]
            rows.append(" | ".join(cells))
        table_text = "\n".join(rows)
        if table_text.strip():
            blocks.append(Block("table", table_text))

    return blocks

def extract_pdf(path: str) -> list[Block]:
    import pdfplumber
    blocks = []

    with pdfplumber.open(path) as pdf:
        for page_num, page in enumerate(pdf.pages, 1):
            words = page.extract_words(extra_attrs=["size", "fontname", "flags"])
            if not words:
                # Textless page (either image or empty)
                continue

            sizes = [w["size"] for w in words if w.get("size")]
            if not sizes:
                continue
            avg_size = sum(sizes) / len(sizes)

            lines = group_words_into_lines(words)

            for line in lines:
                text = " ".join(w["text"] for w in line).strip()
                if not text:
                    continue

                line_size = line[0].get("size", avg_size)
                fontname = line[0].get("fontname", "").lower()
                is_bold = "bold" in fontname or "black" in fontname or "heavy" in fontname

                # Filter out pure noise/page numbers
                if len(text) < 10 and text.isdigit() and abs(line[0]['bottom'] - page.height) < 50:
                    continue # Likely a footer/page number

                if line_size > avg_size * 1.4 or (line_size > avg_size * 1.25 and is_bold):
                    blocks.append(Block("heading1", text, page=page_num, level=1))
                elif line_size > avg_size * 1.15 and is_bold:
                    blocks.append(Block("heading2", text, page=page_num, level=2))
                elif is_bold and line_size >= avg_size:
                    if len(text) < 150: # Only relatively short bold lines are headings
                        blocks.append(Block("heading3", text, page=page_num, level=3))
                    else:
                        blocks.append(Block("paragraph", text, page=page_num))
                else:
                    # check for bullets
                    if text.startswith(("•", "-", "*", "o", "○", "1.", "2.", "3.", "a.", "b.")):
                        blocks.append(Block("list_item", text, page=page_num))
                    else:
                        blocks.append(Block("paragraph", text, page=page_num))

            tables = page.extract_tables()
            for table in tables:
                rows = []
                for row in table:
                    merged = " | ".join(str(cell).strip() if cell else "" for cell in row)
                    rows.append(merged)
                if rows:
                    blocks.append(Block("table", "\n".join(rows), page=page_num))

    return blocks

def group_words_into_lines(words: list, y_tolerance: float = 3.0) -> list:
    if not words:
        return []
    lines = []
    current_line = [words[0]]
    for word in words[1:]:
        # If words are on roughly the same horizontal line
        if abs(word["top"] - current_line[-1]["top"]) <= y_tolerance or \
           abs(word["bottom"] - current_line[-1]["bottom"]) <= y_tolerance:
            current_line.append(word)
        else:
            lines.append(sorted(current_line, key=lambda w: w["x0"]))
            current_line = [word]
    if current_line:
        lines.append(sorted(current_line, key=lambda w: w["x0"]))
    return lines

def is_docx_by_magic(path):
    with open(path, "rb") as f:
        return f.read(4).startswith(b"PK\x03\x04")

def is_pdf_by_magic(path):
    with open(path, "rb") as f:
        return f.read(4).startswith(b"%PDF")

def main():
    sys.stdout.reconfigure(encoding='utf-8')
    if len(sys.argv) < 2:
        print(json.dumps({"error": "No file path provided"}))
        sys.exit(1)

    path = sys.argv[1]
    
    if not os.path.exists(path):
        print(json.dumps({"error": f"File not found: {path}"}))
        sys.exit(1)

    try:
        if is_pdf_by_magic(path):
            blocks = extract_pdf(path)
        elif is_docx_by_magic(path):
            blocks = extract_docx(path)
        else:
            ext = Path(path).suffix.lower()
            if ext == ".pdf":
                blocks = extract_pdf(path)
            elif ext in (".docx", ".doc"):
                blocks = extract_docx(path)
            else:
                blocks = []
                print(json.dumps({"error": f"Unsupported format and magic bytes did not match: {ext}"}))
                sys.exit(1)
        
        # If empty extraction, return a special block to signal empty
        if not blocks:
            blocks = [Block("empty", "", level=0)]

        print(json.dumps([asdict(b) for b in blocks], ensure_ascii=False))
        sys.exit(0)
    except Exception as e:
        print(json.dumps({"error": str(e)}))
        sys.exit(1)

if __name__ == "__main__":
    main()
