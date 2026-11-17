"""Generate safe synthetic OCR fixtures and original-Python preprocessing baselines.

Requires Pillow and pytesseract only when regenerating fixtures, not for Go tests.
The reference argument must name the original mcp-serial-hid-kvm ocr.py source.
"""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import warnings

from PIL import Image, ImageDraw, ImageFont, __version__ as pillow_version

parser = argparse.ArgumentParser()
parser.add_argument("--reference", type=Path, required=True)
parser.add_argument("--tesseract", default="tesseract")
parser.add_argument("--font", type=Path, default=Path("/usr/share/fonts/truetype/liberation/LiberationMono-Regular.ttf"))
args = parser.parse_args()
warnings.filterwarnings("ignore", category=DeprecationWarning)
spec = importlib.util.spec_from_file_location("original_ocr", args.reference)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
engine = module.TerminalOCR(args.tesseract)
font = ImageFont.truetype(str(args.font), 24)
fixtures = [
    ("linux-console", (0, 0, 0), (215, 215, 215),
     "Autopaws Linux console\nBuild status: PASS\nVideo input: 1920 x 1080"),
    ("dark-terminal", (18, 18, 18), (170, 220, 170),
     "agent@linux:~$ printf hello\nhello autopaws\nFiles: README.md main.go"),
    ("light-terminal", (250, 250, 245), (20, 30, 20),
     "user@host:~$ uname\nLinux\nCapture stream ready"),
    ("powershell-style", (10, 35, 80), (225, 225, 225),
     "PS C:\\Autopaws> Write-Output hello\nhello autopaws\nStatus: Ready"),
]
root = Path(__file__).resolve().parent
records = []
for name, background, foreground, expected in fixtures:
    image = Image.new("RGB", (720, 140), background)
    drawing = ImageDraw.Draw(image)
    for index, line in enumerate(expected.splitlines()):
        drawing.text((20, 14 + index * 38), line, font=font, fill=foreground)
    jpeg = root / (name + ".jpg")
    image.save(jpeg, quality=95, subsampling=0)
    with Image.open(jpeg) as decoded:
        processed = engine.preprocess_image(decoded)
        processed.save(root / (name + ".python.pgm"), format="PPM")
        python_text = engine.extract_text(decoded)
    records.append({"name": name, "jpeg": jpeg.name, "python_pgm": name + ".python.pgm",
                    "expected": expected, "original_python_text": python_text})
(root / "corpus.json").write_text(json.dumps(records, indent=2) + "\n")
metadata = {
    "synthetic": True,
    "description": "Controlled text rendered with a public font; no target screenshots or credentials. PowerShell is a visual style, not Windows runtime validation.",
    "font": args.font.name,
    "font_sha256": hashlib.sha256(args.font.read_bytes()).hexdigest(),
    "font_size": 24,
    "pillow_version": pillow_version,
    "original_ocr_source_sha256": hashlib.sha256(args.reference.read_bytes()).hexdigest(),
    "tesseract_version": subprocess.check_output([args.tesseract, "--version"], text=True).splitlines()[0],
}
(root / "provenance.json").write_text(json.dumps(metadata, indent=2) + "\n")
