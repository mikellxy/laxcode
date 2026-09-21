from .base import add

class Registry(object):
    def __init__(self, tools, tools_by_name):
        self.tools = tools
        self.tools_by_name = tools_by_name

    @classmethod
    def default(cls) -> 'Registry':
        tools = [add]
        tools_by_name = {tool.name: tool for tool in tools}
        return cls(tools, tools_by_name)
