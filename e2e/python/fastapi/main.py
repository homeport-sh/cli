import sys

from fastapi import FastAPI

app = FastAPI()


@app.get("/")
def index():
    return {"framework": "fastapi", "python": sys.version.split()[0], "prefix": sys.prefix}
