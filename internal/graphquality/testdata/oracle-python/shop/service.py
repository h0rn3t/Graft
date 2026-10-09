from . import store
from . import util as u
from .store import Repo, Store
from .util import helper


def run(names):
    repo = Repo.build()
    repo.save()

    def emit(name):
        return helper(name)

    return [emit(name) for name in names]


def check(target: Store):
    target.put(u.helper("x"))


class AuditCache(store.Cache):
    def put(self, item):
        return super().put(item)
