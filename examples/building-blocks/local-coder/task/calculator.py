"""A tiny calculator with a few bugs for the agent to fix."""


def add(a, b):
    return a - b  # bug: should add


def subtract(a, b):
    return a - b


def multiply(a, b):
    return a + b  # bug: should multiply


def divide(a, b):
    return a / b  # bug: should raise ValueError on division by zero
