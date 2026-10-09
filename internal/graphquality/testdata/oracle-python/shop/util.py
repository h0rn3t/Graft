def helper(value):
    return normalize(value)


def normalize(value):
    return [clean(part) for part in value.split(",")]


def clean(part):
    return part.strip()
