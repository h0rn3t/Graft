import functools


def by_name(target, name):
    return getattr(target, name)()


def make_saver(repo):
    def saver():
        return repo.save()

    return saver


def dispatch(kind, store, cache):
    handlers = {"store": store.put, "cache": cache.put}
    return handlers[kind]("row")


def deferred(store, repo):
    later = functools.partial(store.put, "row")
    later()
    return make_saver(repo)()
