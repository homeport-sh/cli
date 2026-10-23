import sys

from django.http import JsonResponse
from django.urls import path


# the front-end's build (package.json) makes static/build/app.css from the
# classes it finds here
PAGE_CLASSES = "text-emerald-600 font-semibold"


def index(request):
    return JsonResponse({"framework": "django", "python": sys.version.split()[0], "prefix": sys.prefix})


urlpatterns = [path("", index)]
