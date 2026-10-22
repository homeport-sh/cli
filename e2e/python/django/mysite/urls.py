import sys

from django.http import JsonResponse
from django.urls import path


def index(request):
    return JsonResponse({"framework": "django", "python": sys.version.split()[0], "prefix": sys.prefix})


urlpatterns = [path("", index)]
