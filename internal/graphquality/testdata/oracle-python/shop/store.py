class Store:
    def __init__(self):
        self.items = []

    def put(self, item):
        self.items.append(item)
        self.touch()

    def touch(self):
        return len(self.items)


class Cache:
    def put(self, item):
        return item


class BaseRepo:
    def save(self):
        return "saved"

    def describe(self):
        return "repo"


class Repo(BaseRepo):
    def __init__(self):
        self.store = Store()

    def save(self):
        self.store.put("row")
        return super().save()

    def flush(self, cache: Cache):
        cache.put("row")
        return self.describe()

    @staticmethod
    def build():
        return Repo()
