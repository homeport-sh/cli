import sys

from flask import Flask

app = Flask(__name__)


@app.get("/")
def index():
    return {"framework": "flask", "python": sys.version.split()[0], "prefix": sys.prefix}
